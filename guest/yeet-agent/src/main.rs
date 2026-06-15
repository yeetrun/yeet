fn main() {
    if let Err(err) = yeet_agent::serve_vsock_forever(yeet_agent::AGENT_PORT) {
        eprintln!("yeet-agent: {err}");
        std::process::exit(1);
    }
}
