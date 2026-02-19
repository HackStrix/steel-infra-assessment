use crate::client::OrchestratorClient;
use crate::runner::TestCase;

/// Register worker failure recovery test cases.
pub fn tests() -> Vec<TestCase> {
    vec![TestCase {
        name: "Worker failure recovery".to_string(),
        func: Box::new(|client: &OrchestratorClient| {
            Box::pin(test_worker_recovery(client))
        }),
    }]
}

/// Verify the orchestrator can recover from worker churn.
///
/// Strategy: Create sessions to consume workers, delete them all to free
/// them up, then create new sessions to prove the pool is still functional.
async fn test_worker_recovery(client: &OrchestratorClient) -> Result<(), String> {
    // Phase 1: Create several sessions to consume workers
    let mut session_ids = Vec::new();
    for i in 0..3 {
        let data = serde_json::json!({"user": format!("recovery_{i}")});
        match client.create_session(data).await {
            Ok(s) => session_ids.push(s.id),
            Err(e) => return Err(format!("phase 1: failed to create session {i}: {e}")),
        }
    }

    // Phase 2: Delete all sessions to free workers
    for id in &session_ids {
        let _ = client.delete_session(id).await;
    }

    // Small delay to let the pool settle
    tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;

    // Phase 3: Verify we can still create sessions (pool recovered)
    let mut new_ids = Vec::new();
    for i in 0..3 {
        let data = serde_json::json!({"user": format!("recovery_post_{i}")});
        match client.create_session(data).await {
            Ok(s) => new_ids.push(s.id),
            Err(e) => {
                // Cleanup what we managed to create
                for nid in &new_ids {
                    let _ = client.delete_session(nid).await;
                }
                return Err(format!("phase 3: pool failed to recover, session {i}: {e}"));
            }
        }
    }

    // Cleanup
    for id in &new_ids {
        let _ = client.delete_session(id).await;
    }

    Ok(())
}
