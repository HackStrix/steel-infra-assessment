use colored::Colorize;
use std::future::Future;
use std::pin::Pin;

use crate::client::OrchestratorClient;

/// A single test case: a name and an async closure that returns Ok(()) on success.
pub struct TestCase {
    pub name: String,
    pub func: Box<
        dyn Fn(&OrchestratorClient) -> Pin<Box<dyn Future<Output = Result<(), String>> + '_>>
            + Send
            + Sync,
    >,
}

/// Collects and runs test cases, tracking pass/fail counts.
pub struct TestRunner {
    groups: Vec<(&'static str, Vec<TestCase>)>,
}

impl TestRunner {
    pub fn new() -> Self {
        Self { groups: Vec::new() }
    }

    /// Register a named group of test cases.
    pub fn add_group(&mut self, name: &'static str, tests: Vec<TestCase>) {
        self.groups.push((name, tests));
    }

    /// Run all test groups sequentially and print results.
    /// Returns (passed, total).
    pub async fn run(&self, client: &OrchestratorClient) -> (usize, usize) {
        let mut passed = 0usize;
        let mut total = 0usize;

        for (group_name, tests) in &self.groups {
            println!("\n  {} {}", "▸".dimmed(), group_name.bold());

            for test in tests {
                total += 1;
                match (test.func)(client).await {
                    Ok(()) => {
                        passed += 1;
                        println!("    {} {}", "✓".green(), test.name);
                    }
                    Err(e) => {
                        println!("    {} {}: {}", "✗".red(), test.name.red(), e);
                    }
                }
            }
        }

        (passed, total)
    }
}
