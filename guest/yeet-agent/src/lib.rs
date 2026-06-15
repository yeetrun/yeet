use serde::{Deserialize, Serialize};
use std::fs;
use std::io::{self, BufRead, Write};
use std::net::Ipv4Addr;
use std::path::Path;
use std::process::Command;

#[cfg(target_os = "linux")]
use std::fs::File;
#[cfg(target_os = "linux")]
use std::io::{BufReader, BufWriter};
#[cfg(target_os = "linux")]
use std::mem;
#[cfg(target_os = "linux")]
use std::os::fd::{FromRawFd, RawFd};

pub const PROTOCOL_VERSION: u32 = 1;
pub const AGENT_PORT: u32 = 7788;

#[derive(Debug, Deserialize)]
pub struct AgentRequest {
    pub protocol: u32,
    #[serde(rename = "type")]
    pub request_type: String,
    pub request_id: String,
}

#[derive(Debug, Serialize)]
pub struct AgentResponse {
    pub protocol: u32,
    #[serde(rename = "type")]
    pub response_type: String,
    pub request_id: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub interfaces: Vec<AgentInterface>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<AgentError>,
}

#[derive(Debug, Serialize)]
pub struct AgentError {
    pub code: String,
    pub message: String,
}

#[derive(Debug, Clone, Eq, PartialEq, Serialize)]
pub struct AgentInterface {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mac: String,
    pub up: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub ips: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct IpAddrInterface {
    #[serde(default)]
    ifname: String,
    #[serde(default)]
    address: String,
    #[serde(default)]
    operstate: String,
    #[serde(default)]
    flags: Vec<String>,
    #[serde(default)]
    addr_info: Vec<IpAddrInfo>,
}

#[derive(Debug, Deserialize)]
struct IpAddrInfo {
    #[serde(default)]
    family: String,
    #[serde(default)]
    local: String,
}

const IP_COMMANDS: [&str; 3] = ["/usr/bin/ip", "/run/current-system/sw/bin/ip", "/sbin/ip"];

pub fn usable_interfaces(input: &[AgentInterface]) -> Vec<AgentInterface> {
    input
        .iter()
        .filter(|iface| {
            let name = iface.name.trim();
            iface.up && !name.is_empty() && name != "lo"
        })
        .filter_map(|iface| {
            let ips: Vec<String> = iface.ips.iter().filter_map(|ip| usable_ipv4(ip)).collect();
            if ips.is_empty() {
                None
            } else {
                let mut out = iface.clone();
                out.name = out.name.trim().to_string();
                out.mac = out.mac.trim().to_string();
                out.ips = ips;
                Some(out)
            }
        })
        .collect()
}

fn usable_ipv4(raw: &str) -> Option<String> {
    let ip: Ipv4Addr = raw.trim().parse().ok()?;
    if ip.is_loopback()
        || ip.is_link_local()
        || ip.is_unspecified()
        || ip.is_multicast()
        || ip.octets() == [255, 255, 255, 255]
    {
        return None;
    }
    Some(ip.to_string())
}

pub fn handle_one_request<R: BufRead, W: Write, F>(
    mut reader: R,
    mut writer: W,
    mut network_state: F,
) -> io::Result<()>
where
    F: FnMut() -> io::Result<Vec<AgentInterface>>,
{
    let mut line = String::new();
    if reader.read_line(&mut line)? == 0 {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "agent request stream ended before a request",
        ));
    }
    let req: AgentRequest = serde_json::from_str(&line)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;

    let resp = if req.protocol != PROTOCOL_VERSION {
        response_error(
            &req,
            "protocol_mismatch",
            format!(
                "unsupported protocol version {}; expected {}",
                req.protocol, PROTOCOL_VERSION
            ),
        )
    } else {
        match req.request_type.as_str() {
            "network_state" => match network_state() {
                Ok(interfaces) => AgentResponse {
                    protocol: PROTOCOL_VERSION,
                    response_type: req.request_type.clone(),
                    request_id: req.request_id.clone(),
                    interfaces: usable_interfaces(&interfaces),
                    error: None,
                },
                Err(err) => response_error(&req, "network_state_failed", err.to_string()),
            },
            "ping" | "hello" => AgentResponse {
                protocol: PROTOCOL_VERSION,
                response_type: req.request_type.clone(),
                request_id: req.request_id.clone(),
                interfaces: Vec::new(),
                error: None,
            },
            _ => response_error(&req, "unknown_request", "unknown request type".to_string()),
        }
    };

    serde_json::to_writer(&mut writer, &resp).map_err(json_error_to_io)?;
    writer.write_all(b"\n")?;
    writer.flush()
}

fn response_error(req: &AgentRequest, code: &str, message: String) -> AgentResponse {
    AgentResponse {
        protocol: PROTOCOL_VERSION,
        response_type: req.request_type.clone(),
        request_id: req.request_id.clone(),
        interfaces: Vec::new(),
        error: Some(AgentError {
            code: code.to_string(),
            message,
        }),
    }
}

fn json_error_to_io(err: serde_json::Error) -> io::Error {
    io::Error::new(
        err.io_error_kind().unwrap_or(io::ErrorKind::InvalidData),
        err,
    )
}

pub fn collect_network_state() -> io::Result<Vec<AgentInterface>> {
    let mut last_err = None;
    for path in IP_COMMANDS {
        match Command::new(path)
            .args(["-j", "-4", "addr", "show"])
            .output()
        {
            Ok(output) if output.status.success() => return parse_ip_json(&output.stdout),
            Ok(output) => {
                let stderr = String::from_utf8_lossy(&output.stderr);
                let stderr = stderr.trim();
                let message = if stderr.is_empty() {
                    format!("{path} exited with {}", output.status)
                } else {
                    format!("{path} exited with {}: {stderr}", output.status)
                };
                last_err = Some(io::Error::other(message));
            }
            Err(err) => {
                last_err = Some(io::Error::new(err.kind(), format!("{path}: {err}")));
            }
        }
    }
    Err(last_err.unwrap_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no ip command found")))
}

pub fn parse_ip_json(raw: &[u8]) -> io::Result<Vec<AgentInterface>> {
    parse_ip_json_with_mac_reader(raw, read_mac)
}

fn parse_ip_json_with_mac_reader<F>(raw: &[u8], mut read_mac: F) -> io::Result<Vec<AgentInterface>>
where
    F: FnMut(&str) -> io::Result<String>,
{
    let parsed: Vec<IpAddrInterface> = serde_json::from_slice(raw)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    let mut interfaces = Vec::with_capacity(parsed.len());
    for iface in parsed {
        let name = iface.ifname.trim().to_string();
        let ips = iface
            .addr_info
            .iter()
            .filter(|addr| addr.family == "inet")
            .filter_map(|addr| addr.local.trim().parse::<Ipv4Addr>().ok())
            .map(|ip| ip.to_string())
            .collect();
        let mac = normalized_mac(&iface.address)
            .or_else(|| read_mac(&name).ok().and_then(|mac| normalized_mac(&mac)))
            .unwrap_or_default();
        interfaces.push(AgentInterface {
            name,
            mac,
            up: interface_is_up(&iface),
            ips,
        });
    }
    Ok(usable_interfaces(&interfaces))
}

fn normalized_mac(raw: &str) -> Option<String> {
    let mac = raw.trim();
    if mac.contains(':') {
        Some(mac.to_string())
    } else {
        None
    }
}

fn interface_is_up(iface: &IpAddrInterface) -> bool {
    iface.operstate.eq_ignore_ascii_case("UP")
        || iface
            .flags
            .iter()
            .any(|flag| flag.eq_ignore_ascii_case("UP"))
}

fn read_mac(name: &str) -> io::Result<String> {
    if name.is_empty() || name.contains('/') || name.contains('\0') {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "invalid interface name",
        ));
    }
    let raw = fs::read_to_string(Path::new("/sys/class/net").join(name).join("address"))?;
    Ok(raw.trim().to_string())
}

#[cfg(target_os = "linux")]
pub fn serve_vsock_forever(_port: u32) -> io::Result<()> {
    let fd = listen_vsock(_port)?;
    loop {
        match accept_vsock(fd) {
            Ok(conn_fd) => {
                if let Err(err) = handle_stream_fd(conn_fd) {
                    eprintln!("yeet-agent: connection error: {err}");
                }
            }
            Err(err) if err.kind() == io::ErrorKind::Interrupted => {}
            Err(err) => return Err(err),
        }
    }
}

#[cfg(not(target_os = "linux"))]
pub fn serve_vsock_forever(_port: u32) -> io::Result<()> {
    Err(io::Error::other("vsock requires Linux"))
}

#[cfg(target_os = "linux")]
fn accept_vsock(fd: RawFd) -> io::Result<RawFd> {
    let conn_fd = unsafe { libc::accept(fd, std::ptr::null_mut(), std::ptr::null_mut()) };
    if conn_fd < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(conn_fd)
    }
}

#[cfg(target_os = "linux")]
fn handle_stream_fd(fd: RawFd) -> io::Result<()> {
    let stream = unsafe { File::from_raw_fd(fd) };
    let reader = BufReader::new(stream.try_clone()?);
    let writer = BufWriter::new(stream);
    handle_one_request(reader, writer, collect_network_state)
}

#[cfg(target_os = "linux")]
fn listen_vsock(port: u32) -> io::Result<RawFd> {
    let fd = unsafe { libc::socket(libc::AF_VSOCK, libc::SOCK_STREAM | libc::SOCK_CLOEXEC, 0) };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }

    let addr = libc::sockaddr_vm {
        svm_family: libc::AF_VSOCK as libc::sa_family_t,
        svm_reserved1: 0,
        svm_port: port,
        svm_cid: libc::VMADDR_CID_ANY,
        svm_zero: [0; 4],
    };
    let bind_rc = unsafe {
        libc::bind(
            fd,
            &addr as *const libc::sockaddr_vm as *const libc::sockaddr,
            mem::size_of::<libc::sockaddr_vm>() as libc::socklen_t,
        )
    };
    if bind_rc < 0 {
        let err = io::Error::last_os_error();
        unsafe {
            libc::close(fd);
        }
        return Err(err);
    }
    if unsafe { libc::listen(fd, 16) } < 0 {
        let err = io::Error::last_os_error();
        unsafe {
            libc::close(fd);
        }
        return Err(err);
    }
    Ok(fd)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;
    use std::cell::Cell;

    #[test]
    fn filters_unusable_interfaces_and_normalizes_ipv4_addresses() {
        let got = usable_interfaces(&[
            AgentInterface {
                name: "lo".to_string(),
                mac: String::new(),
                up: true,
                ips: vec!["127.0.0.1".to_string()],
            },
            AgentInterface {
                name: "eth0".to_string(),
                mac: "02:fc:00:00:00:12".to_string(),
                up: true,
                ips: vec![
                    "169.254.1.1".to_string(),
                    "0.0.0.0".to_string(),
                    "224.0.0.1".to_string(),
                    " 10.0.4.183 ".to_string(),
                    "not-an-ip".to_string(),
                ],
            },
            AgentInterface {
                name: "eth1".to_string(),
                mac: String::new(),
                up: false,
                ips: vec!["192.168.1.2".to_string()],
            },
        ]);

        assert_eq!(got.len(), 1);
        assert_eq!(got[0].name, "eth0");
        assert_eq!(got[0].ips, vec!["10.0.4.183"]);
    }

    #[test]
    fn handles_network_state_request() {
        let mut out = Vec::new();
        handle_one_request(
            br#"{"protocol":1,"type":"network_state","request_id":"r1"}
"#
            .as_slice(),
            &mut out,
            || {
                Ok(vec![AgentInterface {
                    name: "eth0".to_string(),
                    mac: "02:fc:00:00:00:12".to_string(),
                    up: true,
                    ips: vec!["10.0.4.183".to_string()],
                }])
            },
        )
        .expect("handle request");

        let resp: Value = serde_json::from_slice(&out).expect("response json");
        assert_eq!(resp["protocol"], PROTOCOL_VERSION);
        assert_eq!(resp["type"], "network_state");
        assert_eq!(resp["request_id"], "r1");
        assert_eq!(resp["interfaces"][0]["name"], "eth0");
        assert_eq!(resp["interfaces"][0]["ips"][0], "10.0.4.183");
        assert!(resp.get("error").is_none());
    }

    #[test]
    fn handles_ping_and_hello_requests() {
        for request_type in ["ping", "hello"] {
            let mut out = Vec::new();
            let input = format!(
                r#"{{"protocol":1,"type":"{request_type}","request_id":"r2"}}
"#
            );
            handle_one_request(input.as_bytes(), &mut out, || Ok(Vec::new()))
                .expect("handle request");

            let resp: Value = serde_json::from_slice(&out).expect("response json");
            assert_eq!(resp["protocol"], PROTOCOL_VERSION);
            assert_eq!(resp["type"], request_type);
            assert_eq!(resp["request_id"], "r2");
            assert!(resp.get("interfaces").is_none());
            assert!(resp.get("error").is_none());
        }
    }

    #[test]
    fn rejects_protocol_mismatch_without_collecting_network_state() {
        let called = Cell::new(false);
        let mut out = Vec::new();
        handle_one_request(
            br#"{"protocol":2,"type":"network_state","request_id":"r3"}
"#
            .as_slice(),
            &mut out,
            || {
                called.set(true);
                Ok(Vec::new())
            },
        )
        .expect("handle request");

        let resp: Value = serde_json::from_slice(&out).expect("response json");
        assert!(!called.get());
        assert_eq!(resp["protocol"], PROTOCOL_VERSION);
        assert_eq!(resp["type"], "network_state");
        assert_eq!(resp["request_id"], "r3");
        assert_eq!(resp["error"]["code"], "protocol_mismatch");
    }

    #[test]
    fn rejects_unknown_request_type() {
        let mut out = Vec::new();
        handle_one_request(
            br#"{"protocol":1,"type":"exec","request_id":"r4"}
"#
            .as_slice(),
            &mut out,
            || Ok(Vec::new()),
        )
        .expect("handle request");

        let resp: Value = serde_json::from_slice(&out).expect("response json");
        assert_eq!(resp["type"], "exec");
        assert_eq!(resp["request_id"], "r4");
        assert_eq!(resp["error"]["code"], "unknown_request");
    }

    #[test]
    fn parses_ip_json_output() {
        let raw = br#"[
          {"ifname":"lo","operstate":"UNKNOWN","flags":["LOOPBACK","UP"],"addr_info":[{"family":"inet","local":"127.0.0.1"}]},
          {"ifname":"eth0","address":"02:fc:00:00:00:12","operstate":"UP","addr_info":[{"family":"inet","local":"10.0.4.183"}]},
          {"ifname":"eth1","operstate":"DOWN","addr_info":[{"family":"inet","local":"192.168.1.2"}]}
        ]"#;

        let got = parse_ip_json(raw).expect("parse");

        assert_eq!(got.len(), 1);
        assert_eq!(got[0].name, "eth0");
        assert_eq!(got[0].mac, "02:fc:00:00:00:12");
        assert_eq!(got[0].ips, vec!["10.0.4.183"]);
    }

    #[test]
    fn parses_ip_json_falls_back_to_sysfs_mac() {
        let raw = br#"[
          {"ifname":"eth0","operstate":"UP","addr_info":[{"family":"inet","local":"10.0.4.183"}]}
        ]"#;

        let got = parse_ip_json_with_mac_reader(raw, |name| {
            assert_eq!(name, "eth0");
            Ok("02:fc:00:00:00:34\n".to_string())
        })
        .expect("parse");

        assert_eq!(got.len(), 1);
        assert_eq!(got[0].mac, "02:fc:00:00:00:34");
    }
}
