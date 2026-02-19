use crate::client::OrchestratorClient;
use crate::runner::TestCase;

/// Register TTL test cases.
pub fn tests() -> Vec<TestCase> {
    vec![TestCase {
        name: "Session TTL expiration (60s)".to_string(),
        func: Box::new(|client: &OrchestratorClient| {
            Box::pin(test_session_ttl(client))
        }),
    }]
}

/// Create a session, wait for the TTL to expire (~65s), then verify GET returns 404.
async fn test_session_ttl(client: &OrchestratorClient) -> Result<(), String> {
    let data = serde_json::json!({"user": "ttl_test"});
    let session = client.create_session(data).await?;

    // The orchestrator TTL is 60s with a 5s sweep interval,
    // so we need to wait at least 65 seconds.
    tokio::time::sleep(tokio::time::Duration::from_secs(67)).await;

    match client.get_session(&session.id).await {
        Err(e) if e == "404" => Ok(()),
        Ok(_) => Err("session still alive after TTL — expected 404".into()),
        Err(e) => Err(format!("unexpected error: {e}")),
    }
}
