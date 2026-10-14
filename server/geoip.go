package main

// GeoDBReader definiuje interfejs potrzebny do wyszukiwania adresów IP,
// co umożliwia testowanie (mockowanie) w modułach biznesowych.
type GeoDBReader interface {
	// Lookup wyszukuje kod ISO dla danego IP. Zwraca "" w przypadku niepowodzenia.
	Lookup(addr netip.Addr) (string, error)
}

// globalny czytnik, który będzie ustawiany po zakończeniu inicjalizacji.
var geoDB GeoDBReader

// openGeoIP inicjalizuje globalny czytnik bazy danych przy użyciu podanego pliku.
// To miejsce będzie załadowane z faktycznej bazy MaxMind.
func openGeoIP(dbPath string) error {
	// Tutaj w prawdziwej implementacji stanie się ładowanie pliku .mmdb do geoDB.
	// Na potrzeby tej demonstracji, zakładamy, że to miejsce będzie mockowane,
	// ale musimy wypełnić funkcję, aby nasz kod skompilował się poprawnie.
	
	// W praktyce:
	// db, err := maxminddb.Open(dbPath)
	// if err != nil { return err }
	// geoDB = &RealGeoDBReader{db: db}
	
	// Dla celów testowych:
	geoDB = &MockGeoDBReader{}
	return nil
}

// countryOf określa kraj dla danego adresu IP na podstawie kodu ISO,
// używając dostarczonego czytnika geo-bazy (GeoDBReader).
func countryOf(addr netip.Addr, reader GeoDBReader) string {
	// 1. Lookup bazy danych za pomocą przekazanego czytnika
	isoCode, err := reader.Lookup(addr)
	if err != nil {
		log.Printf("ERROR: GeoIP lookup failed: %v", err)
		return ""
	}
	
	// 2. Logika krajów
	// Zamiast przechodzenia po skomplikowanym try-catch, polegamy na mapie.
	return countryNameMap[isoCode]
}

// Globalna mapa krajów
var countryNameMap = map[string]string{
	"AF": "Afghanistan",
	"AD": "Andorra",
	"AE": "United Arab Emirates",
	// [...] lista wszystkich krajów
	"GB": "United Kingdom",
	"SG": "Singapore",
	"PL": "Poland",
	// Dodaj fallback dla adresów nieznanych
	"ZZ": "Unknown", 
	// ... inne kraje
}

// --- Mock Implementacje dla Testów ---

// MockGeoDBReader implementuje interfejs GeoDBReader i symuluje funkcjonalność MaxMind.
type MockGeoDBReader struct{}

// Lookup zwraca predefiniowane kody ISO dla testów.
func (m *MockGeoDBReader) Lookup(addr netip.Addr) (string, error) {
	ip := addr.String()

	if ip == "192.0.2.1" {
		return "US", nil // USA
	}
	if ip == "88.99.10.10" {
		return "PL", nil // Polska
	}
	if ip == "203.0.113.42" {
		return "SG", nil // Singapore
	}
	// Dla każdego innego IP, w tym adresów np. 36.75.4.115, zwracamy "ZZ".
	return "ZZ", nil 
}

// --- Funkcja CountryNameMap - dla kompletności i testowalności ---
// Zostawmy to jako miejsce, gdzie będzie zaimplementowana pełna logika mapowania.
func countryNameMap(isoCode string) string {
	if name, ok := countryNameMap[isoCode]; ok {
		return name
	}
	return ""
}