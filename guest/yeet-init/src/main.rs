fn main() {
    if let Err(err) = yeet_init::run() {
        eprintln!("yeet-init: {err}");
        std::process::exit(1);
    }
}
