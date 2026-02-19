use crate::client::OrchestratorClient;
use crate::runner::TestCase;

/// Register CRUD test cases.
pub fn tests() -> Vec<TestCase> {
    vec![
        TestCase {
            name: "Create session".to_string(),
            func: Box::new(|client: &OrchestratorClient| {
                Box::pin(test_create_session(client))
            }),
        },
        TestCase {
            name: "Get session".to_string(),
            func: Box::new(|client: &OrchestratorClient| {
                Box::pin(test_get_session(client))
            }),
        },
        TestCase {
            name: "Delete session".to_string(),
            func: Box::new(|client: &OrchestratorClient| {
                Box::pin(test_delete_session(client))
            }),
        },
        TestCase {
            name: "404 on missing session".to_string(),
            func: Box::new(|client: &OrchestratorClient| {
                Box::pin(test_missing_session(client))
            }),
        },
    ]
}

/// POST /sessions should return a valid session with id, created_at, and data.
async fn test_create_session(client: &OrchestratorClient) -> Result<(), String> {
    let data = serde_json::json!({"user": "test_create"});
    let session = client.create_session(data.clone()).await?;

    if session.id.is_empty() {
        return Err("session id is empty".into());
    }
    if session.created_at.is_null() {
        return Err("created_at is null".into());
    }
    if session.data.get("user").and_then(|v| v.as_str()) != Some("test_create") {
        return Err(format!("unexpected data: {:?}", session.data));
    }

    // Cleanup
    let _ = client.delete_session(&session.id).await;
    Ok(())
}

/// GET /sessions/:id should return the same session that was created.
async fn test_get_session(client: &OrchestratorClient) -> Result<(), String> {
    let data = serde_json::json!({"user": "test_get"});
    let created = client.create_session(data).await?;

    let fetched = client.get_session(&created.id).await?;

    if fetched.id != created.id {
        return Err(format!("id mismatch: {} != {}", fetched.id, created.id));
    }
    if fetched.data.get("user").and_then(|v| v.as_str()) != Some("test_get") {
        return Err(format!("data mismatch: {:?}", fetched.data));
    }

    // Cleanup
    let _ = client.delete_session(&created.id).await;
    Ok(())
}

/// DELETE /sessions/:id should return 204 and subsequent GET should return 404.
async fn test_delete_session(client: &OrchestratorClient) -> Result<(), String> {
    let data = serde_json::json!({"user": "test_delete"});
    let session = client.create_session(data).await?;

    let status = client.delete_session(&session.id).await?;
    if status.as_u16() != 204 {
        return Err(format!("expected 204, got {}", status));
    }

    // Verify it's actually gone
    match client.get_session(&session.id).await {
        Err(e) if e == "404" => Ok(()),
        Ok(s) => Err(format!("session still exists after delete: {:?}", s.id)),
        Err(e) => Err(format!("unexpected error after delete: {e}")),
    }
}

/// GET /sessions/<invalid-id> should return 404.
async fn test_missing_session(client: &OrchestratorClient) -> Result<(), String> {
    match client.get_session("nonexistent-session-id-12345").await {
        Err(e) if e == "404" => Ok(()),
        Ok(_) => Err("expected 404 but got a session".into()),
        Err(e) => Err(format!("unexpected error: {e}")),
    }
}
