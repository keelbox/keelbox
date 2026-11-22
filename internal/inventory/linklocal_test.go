package inventory

import (
	"fmt"
	"strings"
	"testing"
)

// Connected sockets whose peer is link-local, once each, with the socket's
// owner: ss leaves "uid:" out for root's.
func TestParseLinkLocalPeers(t *testing.T) {
	passwd := "root:x:0:0::/root:/bin/bash\nsystemd-resolve:x:992:992::/:/usr/sbin/nologin\ndo-agent:x:115:115::/nonexistent:/usr/sbin/nologin\n"
	lines := strings.Split(`tcp   ESTAB  0 0 10.0.0.5:44321 169.254.169.254:80 users:(("do-agent",pid=700,fd=9)) timer:(keepalive,29sec,0) uid:115 ino:1 sk:1 cgroup:/system.slice/do-agent.service <->
tcp   ESTAB  0 0 10.0.0.5:44399 169.254.169.254:80 users:(("do-agent",pid=700,fd=11)) uid:115 ino:9 sk:9 <->
udp   ESTAB  0 0 10.0.0.5:39000 169.254.169.254:53 users:(("systemd-resolve",pid=300,fd=12)) uid:992 ino:3 sk:3 <->
tcp   ESTAB  0 0 10.0.0.5:44400 169.254.169.254:80 users:(("google_guest_ag",pid=400,fd=7)) ino:5 sk:5 <->
tcp   ESTAB  0 0 [fe80::5]%eth0:44401 [fe80::1]%eth0:8080 users:(("python3",pid=2,fd=5)) uid:1001 ino:6 sk:6 <->
udp   ESTAB  0 0 [fe80::5%eth0]:546 [fe80::2%eth0]:547 uid:998 ino:8 sk:8 <->
tcp   ESTAB  0 0 10.0.0.5:22 10.0.0.9:50000 users:(("sshd",pid=5,fd=4)) ino:7 sk:7 <->
tcp   ESTAB  0 0 127.0.0.1:5000 127.0.0.1:40000 users:(("python3",pid=2,fd=6)) uid:1001 ino:10 sk:10 <->
tcp   LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=5,fd=3))
not a socket line`, "\n")
	got := fmt.Sprint(ParseLinkLocalPeers(lines, passwd))
	want := "[{google_guest_ag 0 root tcp 169.254.169.254:80 80} {do-agent 115 do-agent tcp 169.254.169.254:80 80} " +
		"{systemd-resolve 992 systemd-resolve udp 169.254.169.254:53 53} { 998  udp [fe80::2]:547 547} {python3 1001  tcp [fe80::1]:8080 8080}]"
	if got != want {
		t.Errorf("peers %s\nwant  %s", got, want)
	}
	if got := ParseLinkLocalPeers(nil, ""); len(got) != 0 {
		t.Errorf("no lines: %v", got)
	}
}
