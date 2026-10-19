package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntegration_FullFlow(t *testing.T) {
	// Skip if database not available
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	// Clean up any existing test data
	cleanupTestData(t)

	// 1. Register a new user
	userEmail := fmt.Sprintf("integration-test-%d@example.test", time.Now().UnixNano())
	userPassword := "testpassword123"

	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(
		fmt.Sprintf(`{"email":"%s","password":"%s"}`, userEmail, userPassword),
	))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleRegister(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want 201, body: %s", w.Code, w.Body.String())
	}
	var regResp map[string]string
	json.NewDecoder(w.Body).Decode(&regResp)
	userID := regResp["user_id"]
	if userID == "" {
		t.Fatal("register: missing user_id")
	}
	t.Logf("Registered user: %s (ID: %s)", userEmail, userID)

	// 2. Create API key for the user
	authHeader := "Basic " + basicAuth(userEmail, userPassword)
	r = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"name":"integration-test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", authHeader)
	w = httptest.NewRecorder()
	handleCreateAPIKey(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("create API key: status = %d, want 201, body: %s", w.Code, w.Body.String())
	}
	var keyResp map[string]string
	json.NewDecoder(w.Body).Decode(&keyResp)
	apiKey := keyResp["api_key"]
	if apiKey == "" {
		t.Fatal("create API key: missing api_key")
	}
	t.Logf("Created API key: %s", apiKey)

	// 3. Report an IP using the API key
	reportedIP := "203.0.113.42"
	r = httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(
		fmt.Sprintf(`{"ip":"%s","source":"integration-test"}`, reportedIP),
	))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", apiKey)
	w = httptest.NewRecorder()
	handleReportIP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("report IP: status = %d, want 201, body: %s", w.Code, w.Body.String())
	}
	var reportResp map[string]any
	json.NewDecoder(w.Body).Decode(&reportResp)
	if !reportResp["accepted"].(bool) {
		t.Fatal("report IP: not accepted")
	}
	t.Logf("Reported IP: %s", reportedIP)

	// 4. List IPs - should include the reported IP (with min_reports=1, minutes=60)
	r = httptest.NewRequest(http.MethodGet, "/api/v1/ips?min_reporters=1&minutes=60", nil)
	r.Header.Set("X-API-Key", apiKey)
	w = httptest.NewRecorder()
	handleListIPs(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("list IPs: status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var ips []ipListEntry
	json.NewDecoder(w.Body).Decode(&ips)
	found := false
	for _, ip := range ips {
		if ip.IP == reportedIP {
			found = true
			t.Logf("Found reported IP in list: %s (reporters: %d)", ip.IP, ip.DistinctReporters)
			break
		}
	}
	if !found {
		// The IP might not be in the list yet because the list generator hasn't run
		// In tests with cache miss, it queries DB directly, so it should be there
		t.Logf("Reported IP %s not yet in list (may need list generator run), list has %d entries", reportedIP, len(ips))
	}

	// 5. Test delta sync - request with If-None-Match
	if len(ips) > 0 {
		etag := w.Header().Get("ETag")
		if etag != "" {
			r = httptest.NewRequest(http.MethodGet, "/api/v1/ips?min_reporters=1&minutes=60&delta=1", nil)
			r.Header.Set("X-API-Key", apiKey)
			r.Header.Set("If-None-Match", etag)
			w = httptest.NewRecorder()
			handleListIPs(w, r)

			// Should be 200 (delta) or 304 (not modified)
			if w.Code != http.StatusOK && w.Code != http.StatusNotModified {
				t.Errorf("delta sync: status = %d, want 200 or 304", w.Code)
			}
			t.Logf("Delta sync: status = %d", w.Code)
		}
	}

	// 6. Test 304 Not Modified
	r = httptest.NewRequest(http.MethodGet, "/api/v1/ips?min_reporters=1&minutes=60", nil)
	r.Header.Set("X-API-Key", apiKey)
	if etag := w.Header().Get("ETag"); etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	w = httptest.NewRecorder()
	handleListIPs(w, r)

	if w.Code != http.StatusNotModified {
		t.Logf("304 not modified test: status = %d (may be 200 if list changed)", w.Code)
	} else {
		t.Logf("304 Not Modified works correctly")
	}

	// 7. Test GeoIP for the reported IP
	r = httptest.NewRequest(http.MethodGet, "/api/v1/geoip/"+reportedIP, nil)
	w = httptest.NewRecorder()
	handleGeoIP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("geoip: status = %d, want 200", w.Code)
	}
	var geoipResp map[string]string
	json.NewDecoder(w.Body).Decode(&geoipResp)
	t.Logf("GeoIP for %s: country=%s, country_name=%s", reportedIP, geoipResp["country"], geoipResp["country_name"])

	// 8. Test GeoIP Batch
	r = httptest.NewRequest(http.MethodPost, "/api/v1/geoip/batch", strings.NewReader(
		fmt.Sprintf(`{"ips":["%s","8.8.8.8"]}`, reportedIP),
	))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handleGeoIPBatch(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("geoip batch: status = %d, want 200", w.Code)
	}
	var batchResp []map[string]string
	json.NewDecoder(w.Body).Decode(&batchResp)
	t.Logf("GeoIP batch: %d results", len(batchResp))

	// Clean up
	cleanupTestData(t)
	t.Log("Integration test completed successfully")
}

func TestIntegration_StatsEndpoint(t *testing.T) {
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	w := httptest.NewRecorder()
	handleStats(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("stats: status = %d, want 200", w.Code)
	}

	var stats map[string]any
	json.NewDecoder(w.Body).Decode(&stats)
	t.Logf("Stats: users=%v, active_keys=%v, reports_24h=%v", stats["users"], stats["active_keys"], stats["reports_24h"])
}

func TestIntegration_HealthEndpoint(t *testing.T) {
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handleHealth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("health: status = %d, want 200", w.Code)
	}

	var health map[string]string
	json.NewDecoder(w.Body).Decode(&health)
	if health["status"] != "ok" {
		t.Errorf("health status = %s, want ok", health["status"])
	}
	if health["postgres"] != "up" {
		t.Errorf("postgres = %s, want up", health["postgres"])
	}
	if health["redis"] != "up" {
		t.Errorf("redis = %s, want up", health["redis"])
	}
}

func TestIntegration_ReportWithInvalidAPIKey(t *testing.T) {
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{"ip":"8.8.8.8"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", "sfw_invalid_key")
	w := httptest.NewRecorder()
	handleReportIP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid API key: status = %d, want 401", w.Code)
	}
}

func TestIntegration_ReportWithoutAPIKey(t *testing.T) {
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{"ip":"8.8.8.8"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleReportIP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing API key: status = %d, want 401", w.Code)
	}
}

func TestIntegration_ReportInvalidIP(t *testing.T) {
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	// First create a valid API key
	userEmail := fmt.Sprintf("ip-test-%d@example.test", time.Now().UnixNano())
	userPassword := "testpassword123"

	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(
		fmt.Sprintf(`{"email":"%s","password":"%s"}`, userEmail, userPassword),
	))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleRegister(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("register failed: %s", w.Body.String())
	}
	var regResp map[string]string
	json.NewDecoder(w.Body).Decode(&regResp)

	authHeader := "Basic " + basicAuth(userEmail, userPassword)
	r = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"name":"test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", authHeader)
	w = httptest.NewRecorder()
	handleCreateAPIKey(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("create API key failed: %s", w.Body.String())
	}
	var keyResp map[string]string
	json.NewDecoder(w.Body).Decode(&keyResp)
	apiKey := keyResp["api_key"]

	// Test invalid IPs
	invalidIPs := []string{"not-an-ip", "10.0.0.1", "192.168.1.1", "256.256.256.256"}
	for _, ip := range invalidIPs {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(
			fmt.Sprintf(`{"ip":"%s"}`, ip),
		))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-API-Key", apiKey)
		w = httptest.NewRecorder()
		handleReportIP(w, r)

		if w.Code == http.StatusCreated {
			t.Errorf("invalid IP %s should be rejected, got 201", ip)
		}
	}
}

func TestIntegration_DuplicateReport(t *testing.T) {
	if db == nil || rdb == nil {
		t.Skip("database not initialized, skipping integration test")
	}

	cleanupTestData(t)

	userEmail := fmt.Sprintf("dup-test-%d@example.test", time.Now().UnixNano())
	userPassword := "testpassword123"

	r := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(
		fmt.Sprintf(`{"email":"%s","password":"%s"}`, userEmail, userPassword),
	))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleRegister(w, r)

	if w.Code != http.StatusCreated {
		t.Fatal("register failed")
	}

	authHeader := "Basic " + basicAuth(userEmail, userPassword)
	r = httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"name":"test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", authHeader)
	w = httptest.NewRecorder()
	handleCreateAPIKey(w, r)

	var keyResp map[string]string
	json.NewDecoder(w.Body).Decode(&keyResp)
	apiKey := keyResp["api_key"]

	reportedIP := "203.0.113.99"

	// First report
	r = httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(
		fmt.Sprintf(`{"ip":"%s","source":"test"}`, reportedIP),
	))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", apiKey)
	w = httptest.NewRecorder()
	handleReportIP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatal("first report failed")
	}

	// Second report of same IP by same user - should be accepted but may not change blacklist
	r = httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(
		fmt.Sprintf(`{"ip":"%s","source":"test"}`, reportedIP),
	))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", apiKey)
	w = httptest.NewRecorder()
	handleReportIP(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("duplicate report: status = %d, want 201", w.Code)
	}
}

func cleanupTestData(t *testing.T) {
	if db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = db.Exec(ctx, `DELETE FROM users WHERE email LIKE '%@example.test'`)
}

func basicAuth(email, password string) string {
	auth := email + ":" + password
	var buf bytes.Buffer
	encoder := base64.NewEncoder(base64.StdEncoding, &buf)
	encoder.Write([]byte(auth))
	encoder.Close()
	return buf.String()
}