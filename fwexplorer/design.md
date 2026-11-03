# Specyfikacja Projektu: Grafikowy Firewall dla OpenWrt (Wersja 0.2)

## 🎯 Cel projektu
Stworzenie graficznego, wizualnego front-endu do monitorowania działania firewalla na OpenWrt.

## 🧱 Technologia (Constraint)
*   **Platforma:** LuCI/Web UI (HTML/JS).
*   **Implikacja:** Design musi być elastyczny, ale technicznie ograniczony do dostępnych standardów LuCI i JavaScriptu działającego w tym kontekście.

## 🛡️ Funkcjonalność (Scope)
*   **Tryb:** Tylko czytanie (Read-Only) + Wizualizacja przepływu.
*   **Implikacja:** Możemy symulować *efekt* edycji, ale nie implementujemy logiki zapisywania zmian do `/etc/config/firewall` – to zostanie zleceniem dla *Phase 2*.

## 📊 Wizualizacja (Level)
*   **Poziom:** Średni: Kontekstualny.
*   **Oczekiwany wynik:** Interfejs, który wykracza poza zwykłą tabelę i wizualnie podświetla *kontekst* decyzji (np. widać, że pakiet z Source A trafił do zasady X, która ma akcję Y).

---
Proszę o ostateczne potwierdzenie, czy ten zarys jest poprawny. Jeśli tak, przejdę do proponowania Layoutu koncepcyjnego.