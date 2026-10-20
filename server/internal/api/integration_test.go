package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"server/internal/db"
	"server/internal/geoip"
)

// mockGeoDB is a test fake of the GeoIP reader with canned answers.
type mockGeoDB struct{}

func (m *mockGeoDB) Lookup(addr netip.Addr) (string, string, error) {
	switch addr.String() {
	case "88.99.10.10":
		return "PL", "Poland", nil
	default:
		return "", "", nil
	}
}

// The integration tests need a real PostgreSQL and Redis; they are skipped
// when the packages are not connected. Set DATABASE_URL and REDIS_ADDR to
// run them: go test -run TestIntegration ./...
func TestMain(m *testing.M) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		ctx := context.Background()
		if err := db.Connect(ctx, dsn, os.Getenv("REDIS_ADDR")); err != nil {
			fmt.Fprintln(os.Stderr, "cannot connect to the database:", err)
			os.Exit(1)
		}
		if err := db.EnsureSchema(ctx, db.DB); err != nil {
			fmt.Fprintln(os.Stderr, "cannot apply the schema:", err)
			os.Exit(1)
		}
		defer db.Close()
	}
	os.Exit(m.Run())
}

func requireDB(t *testing.T) {
	t.Helper()
	if db.DB == nil || db.RDB == nil {
		t.Skip("database not initialized, skipping integration test")
	}
}

func basicAuth(email, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+password))
}

// cleanupTestData removes the test accounts (their keys and reports go with
// them, the blacklist rows stay - they have no delete endpoint).
func cleanupTestData(t *testing.T) {
	t.Helper()
	if db.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = db.DB.Exec(ctx, `DELETE FROM users WHERE email LIKE '%@example.test'`)
}

// testUser registers a user with one API key and returns both.
func testUser(t *testing.T) (email, apiKey string) {
	t.Helper()
	email = fmt.Sprintf("integration-test-%d@example.test", time.Now().UnixNano())
	password := "testpassword123"

	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(
		fmt.Sprintf(`{"email":"%s","password":"%s"}`, email, password)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleRegister(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want 201, body: %s", w.Code, w.Body.String())
	}

	r = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"name":"integration-test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", basicAuth(email, password))
	w = httptest.NewRecorder()
	HandleCreateAPIKey(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create API key: status = %d, want 201, body: %s", w.Code, w.Body.String())
	}
	var keyResp map[string]string
	json.NewDecoder(w.Body).Decode(&keyResp)
	return email, keyResp["api_key"]
}

func TestIntegration_FullFlow(t *testing.T) {
	requireDB(t)
	cleanupTestData(t)
	t.Cleanup(func() { cleanupTestData(t) })

	email, apiKey := testUser(t)

	// Report an IP.
	reportedIP := "203.0.113.42"
	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(
		fmt.Sprintf(`{"ip":"%s","source":"integration-test"}`, reportedIP)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	HandleReportIP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("report IP: status = %d, want 201, body: %s", w.Code, w.Body.String())
	}
	var reportResp map[string]any
	json.NewDecoder(w.Body).Decode(&reportResp)
	if reportResp["accepted"] != true {
		t.Fatal("report IP: not accepted")
	}

	// List IPs: may come from the Redis cache, which does not contain the
	// address yet (the list refreshes every LIST_REFRESH).
	r = httptest.NewRequest(http.MethodGet, "/api/v1/ips?min_reporters=1&minutes=60", nil)
	r.Header.Set("X-API-Key", apiKey)
	w = httptest.NewRecorder()
	HandleListIPs(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list IPs: status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var ips []db.IPListEntry
	if err := json.NewDecoder(w.Body).Decode(&ips); err != nil {
		t.Fatalf("list IPs: decode: %v", err)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Error("list IPs: no ETag header")
	}

	// Conditional request with the ETag: 304 or 200.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/ips?min_reporters=1&minutes=60", nil)
	r.Header.Set("X-API-Key", apiKey)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	HandleListIPs(w, r)
	if w.Code != http.StatusOK && w.Code != http.StatusNotModified {
		t.Errorf("conditional list: status = %d, want 200 or 304", w.Code)
	}

	// GeoIP (the path value is set by the router in the real server).
	r = httptest.NewRequest(http.MethodGet, "/api/v1/geoip/8.8.8.8", nil)
	r.SetPathValue("ip", "8.8.8.8")
	w = httptest.NewRecorder()
	HandleGeoIP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("geoip: status = %d, want 200", w.Code)
	}
	var geoResp map[string]string
	json.NewDecoder(w.Body).Decode(&geoResp)
	if geoResp["country"] == "" {
		t.Error("geoip: no country in the answer")
	}

	// Stats.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	w = httptest.NewRecorder()
	HandleStats(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("stats: status = %d, want 200", w.Code)
	}

	// Health.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w = httptest.NewRecorder()
	HandleHealth(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("health: status = %d, want 200", w.Code)
	}

	// Delete the account.
	r = httptest.NewRequest(http.MethodDelete, "/api/v1/account", nil)
	r.Header.Set("Authorization", basicAuth(email, "testpassword123"))
	w = httptest.NewRecorder()
	HandleDeleteAccount(w, r)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete account: status = %d, want 204", w.Code)
	}
}

func TestIntegration_ReportWithInvalidAPIKey(t *testing.T) {
	requireDB(t)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{"ip":"8.8.8.8"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", "sfw_invalid_key")
	w := httptest.NewRecorder()
	HandleReportIP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid API key: status = %d, want 401", w.Code)
	}
}

func TestIntegration_ReportWithoutAPIKey(t *testing.T) {
	requireDB(t)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{"ip":"8.8.8.8"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleReportIP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing API key: status = %d, want 401", w.Code)
	}
}

func TestIntegration_ReportInvalidIP(t *testing.T) {
	requireDB(t)
	cleanupTestData(t)
	t.Cleanup(func() { cleanupTestData(t) })

	_, apiKey := testUser(t)

	for _, ip := range []string{"not-an-ip", "10.0.0.1", "192.168.1.1", "256.256.256.256"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(
			fmt.Sprintf(`{"ip":"%s"}`, ip)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-API-Key", apiKey)
		w := httptest.NewRecorder()
		HandleReportIP(w, r)
		if w.Code == http.StatusCreated {
			t.Errorf("invalid IP %s should be rejected, got 201", ip)
		}
	}
}

func TestIntegration_GeoIPBatch(t *testing.T) {
	old := geoip.GeoDB
	geoip.GeoDB = &mockGeoDB{}
	t.Cleanup(func() { geoip.GeoDB = old })

	tooMany := `{"ips":[` + strings.TrimSuffix(strings.Repeat(`"8.8.8.8",`, MaxGeoIPBatch+1), ",") + `]}`

	tests := []struct {
		name string
		body string
		code int
		want []map[string]string
	}{
		{
			name: "known, invalid and private",
			body: `{"ips":["88.99.10.10","not-an-ip","10.0.0.1"]}`,
			code: http.StatusOK,
			want: []map[string]string{
				{"ip": "88.99.10.10", "country": "PL", "country_name": "Poland"},
				{"ip": "not-an-ip", "country": "Unknown", "country_name": "Unknown"},
				{"ip": "10.0.0.1", "country": "Unknown", "country_name": "Unknown"},
			},
		},
		{
			name: "empty list",
			body: `{"ips":[]}`,
			code: http.StatusOK,
			want: []map[string]string{},
		},
		{
			name: "over limit",
			body: tooMany,
			code: http.StatusBadRequest,
		},
		{
			name: "not json",
			body: `nope`,
			code: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/geoip/batch", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			HandleGeoIPBatch(w, r)
			if w.Code != tt.code {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.code, w.Body.String())
			}
			if tt.want == nil {
				return
			}
			var got []map[string]string
			if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i]["ip"] != tt.want[i]["ip"] || got[i]["country"] != tt.want[i]["country"] {
					t.Errorf("got %+v, want %+v", got, tt.want)
					break
				}
			}
		})
	}
}

func TestIntegration_WriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusCreated, map[string]string{"a": "b"})
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"a":"b"`)) {
		t.Errorf("body = %s", w.Body.String())
	}
}
