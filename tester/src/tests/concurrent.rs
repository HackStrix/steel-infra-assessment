use crate::client::OrchestratorClient;
use crate::runner::TestCase;

/// Register concurrent test cases.
pub fn tests() -> Vec<TestCase> {
    vec![TestCase {
        name: "Concurrent sessions (10 parallel)".to_string(),
        func: Box::new(|client: &OrchestratorClient| {
            Box::pin(test_concurrent_sessions(client))
        }),
    }]
}

/// Spawn 10 session-create requests simultaneously and verify all succeed.
/// Uses its own reqwest clients per task since OrchestratorClient isn't Send.
async fn test_concurrent_sessions(client: &OrchestratorClient) -> Result<(), String> {
    let count = 10;
    let base_url: String = client.base_url().to_string();
    let mut handles = Vec::with_capacity(count);

    for i in 0..count {
        let url = base_url.clone();
        handles.push(tokio::spawn(async move {
            let http = reqwest::Client::builder()
                .timeout(std::time::Duration::from_secs(35))
                .build()
                .unwrap();
            let data = serde_json::json!({"user": format!("concurrent_{i}")});
            let resp = http
                .post(format!("{url}/sessions"))
                .json(&data)
                .send()
                .await
                .map_err(|e| format!("request {i} failed: {e}"))?;

            if !resp.status().is_success() {
                let status = resp.status();
                let body = resp.text().await.unwrap_or_default();
                return Err(format!("request {i} returned {status}: {body}"));
            }

            let session: crate::client::Session = resp
                .json()
                .await
                .map_err(|e| format!("request {i} parse failed: {e}"))?;
            Ok(session.id)
        }));
    }

    // Collect results
    let mut session_ids = Vec::new();
    let mut errors = Vec::new();

    for handle in handles {
        match handle.await {
            Ok(Ok(id)) => session_ids.push(id),
            Ok(Err(e)) => errors.push(e),
            Err(e) => errors.push(format!("task join error: {e}")),
        }
    }

    // Cleanup all created sessions (always, even if some failed)
    for id in &session_ids {
        let _ = client.delete_session(id).await;
    }

    if !errors.is_empty() {
        return Err(format!(
            "{}/{count} failed: {}",
            errors.len(),
            errors.first().unwrap()
        ));
    }

    if session_ids.len() != count {
        return Err(format!(
            "expected {count} sessions, got {}",
            session_ids.len()
        ));
    }

    // Verify all IDs are unique
    let unique: std::collections::HashSet<&String> = session_ids.iter().collect();
    if unique.len() != count {
        return Err(format!(
            "expected {count} unique session IDs, got {}",
            unique.len()
        ));
    }

    Ok(())
}
