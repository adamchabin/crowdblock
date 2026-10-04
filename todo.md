# TODO List (Feature & Security Backlog)

## 🚧 Implemented / Completed (Ready for Verification)
- [x] Improved UI rendering in `server/ui.html` (Full-screen view).
- [x] Extended country mapping in `server/geoip.go` by dozens of countries (SG, AD, GB, and others).
- [x] Raised CI/CD security level by adding dependency auditing to `server/Dockerfile`.

## 🛠️ To Address (For Refactoring/Optimization)
- [ ] **main.go:** Improve HTTP context management in logs and API logic to ensure full resource isolation between requests.
- [ ] **geoip.go / main.go:** Must address race conditions and improve locking mechanisms for critical shared resources (e.g., global `geoDB` or logging to Redis).
- [ ] **main.go / geoip.go:** Enhance external service failure notification mechanisms (e.g., if Postgres/Redis is unavailable, we should return a more detailed HTTP status than just a general "server error").

## 🧪 To Implement (Next Task)
- [ ] **Unit Tests:** Write a complete suite of tests for the `geoip.go` and `main.go` modules to ensure code coverage and business logic correctness.
- [ ] **Integration Tests:** Create a scenario testing the entire data flow (register -> listIPs -> reportIP).
- [ ] **Performance Optimization:** Refine testing scenarios in `k6/` (load tests) based on API analysis.