use reqwest::{Client, StatusCode};
use serde::{Deserialize, Serialize};

/// Response from POST /sessions and GET /sessions/:id
#[derive(Debug, Deserialize, Serialize)]
pub struct Session {
    pub id: String,
    pub created_at: serde_json::Value,
    pub data: serde_json::Value,
}

/// Typed client for the orchestrator HTTP API.
pub struct OrchestratorClient {
    base_url: String,
    http: Client,
}

impl OrchestratorClient {
    pub fn new(base_url: &str) -> Self {
        Self {
            base_url: base_url.trim_end_matches('/').to_string(),
            http: Client::builder()
                .timeout(std::time::Duration::from_secs(300))
                .build()
                .expect("failed to build HTTP client"),
        }
    }

    /// POST /sessions — create a new session with arbitrary JSON data.
    pub async fn create_session(&self, data: serde_json::Value) -> Result<Session, String> {
        let resp = self
            .http
            .post(format!("{}/sessions", self.base_url))
            .json(&data)
            .send()
            .await
            .map_err(|e| format!("POST /sessions request failed: {e}"))?;

        let status = resp.status();
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            return Err(format!("POST /sessions returned {status}: {body}"));
        }

        resp.json::<Session>()
            .await
            .map_err(|e| format!("failed to parse session response: {e}"))
    }

    /// GET /sessions/:id — retrieve a session by ID.
    pub async fn get_session(&self, id: &str) -> Result<Session, String> {
        let resp = self
            .http
            .get(format!("{}/sessions/{}", self.base_url, id))
            .send()
            .await
            .map_err(|e| format!("GET /sessions/{id} request failed: {e}"))?;

        let status = resp.status();
        if status == StatusCode::NOT_FOUND {
            return Err("404".to_string());
        }
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            return Err(format!("GET /sessions/{id} returned {status}: {body}"));
        }

        resp.json::<Session>()
            .await
            .map_err(|e| format!("failed to parse session response: {e}"))
    }

    /// DELETE /sessions/:id — delete a session. Returns the HTTP status code.
    pub async fn delete_session(&self, id: &str) -> Result<StatusCode, String> {
        let resp = self
            .http
            .delete(format!("{}/sessions/{}", self.base_url, id))
            .send()
            .await
            .map_err(|e| format!("DELETE /sessions/{id} request failed: {e}"))?;

        Ok(resp.status())
    }

    /// GET /health — simple health check.
    pub async fn health(&self) -> Result<String, String> {
        let resp = self
            .http
            .get(format!("{}/health", self.base_url))
            .send()
            .await
            .map_err(|e| format!("GET /health request failed: {e}"))?;

        resp.text()
            .await
            .map_err(|e| format!("failed to read health response: {e}"))
    }

    /// POST /debug/crash-worker?session_id=:id — kills the worker holding the session (testing only).
    pub async fn crash_worker(&self, session_id: &str) -> Result<(), String> {
        let resp = self
            .http
            .post(format!(
                "{}/debug/crash-worker?session_id={}",
                self.base_url, session_id
            ))
            .send()
            .await
            .map_err(|e| format!("POST /debug/crash-worker request failed: {e}"))?;

        let status = resp.status();
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            return Err(format!("crash-worker returned {status}: {body}"));
        }
        Ok(())
    }

    /// Returns the base URL for building custom requests.
    pub fn base_url(&self) -> &str {
        &self.base_url
    }
}
