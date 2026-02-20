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

/// Verify the orchestrator recovers when a worker process is killed mid-session.
///
/// Strategy:
///   1. Create a session to occupy a worker.
///   2. Kill that worker via the debug endpoint — exercises the OnCrash path.
///   3. Verify the crashed session returns 404 (stale mapping cleaned up).
///   4. Verify the pool recovered and can serve new sessions.
async fn test_worker_recovery(client: &OrchestratorClient) -> Result<(), String> {
    // Phase 1: Create a session so a worker is busy
    let data = serde_json::json!({"user": "crash_test"});
    let session = client
        .create_session(data)
        .await
        .map_err(|e| format!("phase 1: failed to create session: {e}"))?;

    // Phase 2: Kill the worker process holding this session
    client
        .crash_worker(&session.id)
        .await
        .map_err(|e| format!("phase 2: failed to crash worker: {e}"))?;

    // Phase 3: Give the orchestrator time to detect the crash and restart the worker
    tokio::time::sleep(tokio::time::Duration::from_secs(3)).await;

    // Phase 4: The crashed session should now return 404
    match client.get_session(&session.id).await {
        Err(e) if e.contains("404") => {}
        Ok(_) => return Err("phase 4: expected 404 for crashed session, got 200".to_string()),
        Err(e) => return Err(format!("phase 4: unexpected error: {e}")),
    }

    // Phase 5: Pool should have recovered — new sessions must be creatable
    let data = serde_json::json!({"user": "post_crash"});
    let new_session = client
        .create_session(data)
        .await
        .map_err(|e| format!("phase 5: pool did not recover after crash: {e}"))?;

    // Cleanup
    let _ = client.delete_session(&new_session.id).await;

    Ok(())
}
