mod client;
mod runner;
mod tests;

use clap::Parser;
use colored::Colorize;

use client::OrchestratorClient;
use runner::TestRunner;

#[derive(Parser)]
#[command(name = "steel-tester", about = "Test suite for the Steel orchestrator")]
struct Args {
    /// Orchestrator base URL
    #[arg(long, default_value = "http://localhost:8080")]
    url: String,
}

#[tokio::main]
async fn main() {
    let args = Args::parse();
    let client = OrchestratorClient::new(&args.url);

    println!();
    println!("{}", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━".bold());
    println!("{}", "🧪 ORCHESTRATOR TEST SUITE".bold());
    println!("{}", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━".bold());

    // Ensure orchestrator is reachable before running tests
    match client.health().await {
        Ok(_) => {}
        Err(e) => {
            eprintln!(
                "\n{} Cannot reach orchestrator at {}: {e}",
                "✗".red(),
                args.url
            );
            std::process::exit(1);
        }
    }

    // Build the test runner with all test groups
    let mut runner = TestRunner::new();
    runner.add_group("CRUD Operations", tests::crud::tests());
    runner.add_group("Concurrency", tests::concurrent::tests());
    runner.add_group("TTL Expiration", tests::ttl::tests());
    runner.add_group("Recovery", tests::recovery::tests());

    // Run all tests
    let (passed, total) = runner.run(&client).await;

    // Print final summary
    println!();
    println!("{}", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━".bold());
    if passed == total {
        println!(
            "{}",
            format!("📊 RESULTS: {passed}/{total} passed").green().bold()
        );
    } else {
        println!(
            "{}",
            format!("📊 RESULTS: {passed}/{total} passed").red().bold()
        );
    }
    println!("{}", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━".bold());
    println!();

    if passed != total {
        std::process::exit(1);
    }
}
