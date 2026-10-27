package arvan

// Every IP Arvan adds to a server arrives on a new NIC whose DHCP default route
// steals the replies of the others, so a front with reserves routes per source.

// routingScript is installed as /usr/local/sbin/x-ui-multi-ip.sh and re-run by
// a timer, because a hotplug or a DHCP renewal puts the stolen routes back.
const routingScript = `#!/bin/sh
# Installed by the 3x-ui panel: one routing table per NIC so every public IP
# answers from its own NIC, and the main default route only via the active IP.
set -u
if [ -z "${XUI_MULTI_IP_LOCKED:-}" ] && command -v flock >/dev/null 2>&1; then
	XUI_MULTI_IP_LOCKED=1
	export XUI_MULTI_IP_LOCKED
	exec flock -w 30 /run/x-ui-multi-ip.lock "$0" "$@"
fi
ROOT=${XUI_MULTI_IP_ROOT:-}
PRIMARY=$(tr -d ' \t\r\n' 2>/dev/null <"$ROOT/etc/x-ui-multi-ip/primary")

# net_of a.b.c.d/len prints the network as a.b.c.0/len.
net_of() {
	n_len=${1#*/}
	set -- $(echo "${1%/*}" | tr . ' ')
	n_int=$(( ($1 << 24) + ($2 << 16) + ($3 << 8) + $4 ))
	n_int=$(( n_int & ((4294967295 << (32 - n_len)) & 4294967295) ))
	echo "$(( (n_int >> 24) & 255 )).$(( (n_int >> 16) & 255 )).$(( (n_int >> 8) & 255 )).$(( n_int & 255 ))/$n_len"
}

# gw_of dev table net/len: the NIC's DHCP gateway, else its table's, else the
# first host of its subnet.
gw_of() {
	g=$(ip -4 route show default table main dev "$1" 2>/dev/null | awk '$2 == "via" {print $3; exit}')
	if [ -z "$g" ] && [ -r "$ROOT/sys/class/net/$1/ifindex" ]; then
		g_idx=$(cat "$ROOT/sys/class/net/$1/ifindex")
		g=$(sed -n 's/^ROUTER=\([0-9.]*\).*/\1/p' "$ROOT/run/systemd/netif/leases/$g_idx" 2>/dev/null | head -n 1)
	fi
	if [ -z "$g" ]; then
		g=$(ip -4 route show default table "$2" 2>/dev/null | awk '$2 == "via" {print $3; exit}')
	fi
	if [ -z "$g" ] && [ "${3#*/}" -lt 31 ]; then
		g_net=${3%/*}
		g="${g_net%.*}.$(( ${g_net##*.} + 1 ))"
	fi
	echo "$g"
}

snapshot() {
	ip -4 route show default table main 2>/dev/null
	ip -4 rule show 2>/dev/null
}
before=$(snapshot)

want=""
act_dev=""
act_ip=""
act_gw=""
eth0_ip=""
eth0_gw=""
any_dev=""
any_ip=""
any_gw=""
for path in "$ROOT"/sys/class/net/eth*; do
	dev=${path##*/}
	num=${dev#eth}
	case $num in '' | *[!0-9]*) continue ;; esac
	[ "$num" -lt 100 ] || continue
	addrs=$(ip -4 -o addr show dev "$dev" scope global 2>/dev/null | awk '{print $4}')
	[ -n "$addrs" ] || continue
	first=$(echo "$addrs" | head -n 1)
	table=$((100 + num))
	cidr=$(net_of "$first")
	gw=$(gw_of "$dev" "$table" "$cidr")
	if [ -z "$gw" ]; then
		echo "x-ui-multi-ip: $dev has no gateway" >&2
		continue
	fi
	ip route replace "$cidr" dev "$dev" src "${first%/*}" scope link table "$table" 2>/dev/null
	ip route replace default via "$gw" dev "$dev" table "$table" 2>/dev/null ||
		ip route replace default via "$gw" dev "$dev" table "$table" onlink || continue
	for a in $addrs; do
		a=${a%/*}
		want="$want $a:$table:$((1000 + num))"
		if [ "$a" = "$PRIMARY" ]; then
			act_dev=$dev
			act_ip=$a
			act_gw=$gw
		fi
	done
	if [ "$dev" = eth0 ]; then
		eth0_ip=${first%/*}
		eth0_gw=$gw
	fi
	if [ -z "$any_dev" ]; then
		any_dev=$dev
		any_ip=${first%/*}
		any_gw=$gw
	fi
done
if [ -z "$act_dev" ] && [ -n "$eth0_ip" ]; then
	act_dev=eth0
	act_ip=$eth0_ip
	act_gw=$eth0_gw
fi
if [ -z "$act_dev" ]; then
	act_dev=$any_dev
	act_ip=$any_ip
	act_gw=$any_gw
fi
if [ -z "$act_dev" ]; then
	echo "x-ui-multi-ip: no eth NIC has an IPv4 address and a gateway" >&2
	exit 1
fi

# Rule 999 keeps every specific route of the main table (tunnels, subnets) in
# force; only the default route is chosen by source address.
rules=$(ip -4 rule show | tr -s '[:blank:]' ' ')
echo "$rules" | grep -q '^999: from all lookup main suppress_prefixlength 0' ||
	ip -4 rule add priority 999 lookup main suppress_prefixlength 0
for w in $want; do
	w_ip=${w%%:*}
	w_rest=${w#*:}
	w_table=${w_rest%%:*}
	w_prio=${w_rest#*:}
	echo "$rules" | grep -qxF "$w_prio: from $w_ip lookup $w_table" ||
		ip -4 rule add priority "$w_prio" from "$w_ip" lookup "$w_table"
done
echo "$rules" | while read -r r_prio r_from r_ip r_lookup r_table r_rest; do
	r_prio=${r_prio%:}
	case $r_prio in '' | *[!0-9]*) continue ;; esac
	[ "$r_prio" -ge 1000 ] && [ "$r_prio" -le 1099 ] || continue
	case " $want " in *" $r_ip:$r_table:$r_prio "*) continue ;; esac
	ip -4 rule del priority "$r_prio" from "$r_ip" lookup "$r_table"
done

# The new default replaces the old one in place; the other NICs' defaults go
# only once it stands, so the box is never without one.
if ! ip route replace default via "$act_gw" dev "$act_dev" src "$act_ip" metric 50; then
	echo "x-ui-multi-ip: could not route the default via $act_dev ($act_ip)" >&2
	exit 1
fi
ip -4 route show default table main | awk -v keep="$act_dev" '{
	dev = ""; args = ""
	for (i = 1; i < NF; i++) {
		if ($i == "dev") dev = $(i + 1)
		if ($i == "via") args = args " via " $(i + 1)
		if ($i == "metric") args = args " metric " $(i + 1)
	}
	if (dev != keep && dev ~ /^eth[0-9]+$/) print args " dev " dev
}' | while read -r d_args; do
	ip route del default $d_args table main
done

after=$(snapshot)
if [ "$before" != "$after" ]; then
	echo "x-ui-multi-ip: active $act_ip on $act_dev via $act_gw"
	echo "$after"
fi
`

// guardActiveIP refuses $1 unless it is an IPv4 on one of this box's NICs, so a
// typo never becomes the primary and drags the default route to eth0.
const guardActiveIP = `case "${1:-}" in
'' | *[!0-9.]*)
	echo "not an IPv4 address: ${1:-}" >&2
	exit 1
	;;
esac
ip -4 -o addr show | awk -v ip="$1" '{split($4, a, "/"); if (a[1] == ip) f = 1} END {exit !f}' || {
	echo "$1 is not on any NIC of this server" >&2
	exit 1
}
`

const installScript = `#!/bin/sh
set -eu
` + guardActiveIP + `
# put PATH MODE: stdin becomes PATH, which is replaced only when it changed.
put() {
	cat >"$1.new"
	chmod "$2" "$1.new"
	if [ -f "$1" ] && cmp -s "$1.new" "$1"; then
		rm -f "$1.new"
	else
		mv -f "$1.new" "$1"
	fi
}
mkdir -p /etc/x-ui-multi-ip /usr/local/sbin /etc/systemd/networkd.conf.d
# A hotplug reconfigures networkd, which by default sweeps routes and rules it
# did not make; from its next start it leaves ours alone.
put /etc/systemd/networkd.conf.d/x-ui-multi-ip.conf 644 <<'XUIUNIT'
[Network]
ManageForeignRoutingPolicyRules=no
ManageForeignRoutes=no
XUIUNIT
put /usr/local/sbin/x-ui-multi-ip.sh 755 <<'XUIROUTE'
` + routingScript + `XUIROUTE
put /etc/systemd/system/x-ui-multi-ip.service 644 <<'XUIUNIT'
[Unit]
Description=x-ui per-IP policy routing
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/x-ui-multi-ip.sh
SyslogLevel=notice
LogLevelMax=notice
XUIUNIT
put /etc/systemd/system/x-ui-multi-ip.timer 644 <<'XUIUNIT'
[Unit]
Description=Re-apply x-ui per-IP policy routing

[Timer]
OnBootSec=15
OnUnitActiveSec=10
AccuracySec=1

[Install]
WantedBy=timers.target
XUIUNIT
printf '%s\n' "$1" | put /etc/x-ui-multi-ip/primary 644
systemctl daemon-reload
systemctl enable -q --now x-ui-multi-ip.timer
/usr/local/sbin/x-ui-multi-ip.sh
echo "x-ui-multi-ip ready"
`

const setPrimaryScript = `#!/bin/sh
set -eu
` + guardActiveIP + `[ -x /usr/local/sbin/x-ui-multi-ip.sh ] || {
	echo "x-ui-multi-ip is not installed here; prepare the server first" >&2
	exit 1
}
mkdir -p /etc/x-ui-multi-ip
printf '%s\n' "$1" >/etc/x-ui-multi-ip/primary.new
mv -f /etc/x-ui-multi-ip/primary.new /etc/x-ui-multi-ip/primary
/usr/local/sbin/x-ui-multi-ip.sh
ip route show default
ip -4 route show default table main | awk -v ip="$1" '{for (i = 1; i < NF; i++) if ($i == "src" && $(i + 1) == ip) f = 1} END {exit !f}' || {
	echo "the default route still does not leave from $1" >&2
	exit 1
}
`

// InstallScript prepares a front for several IPs; $1 is the active IP. It
// prints "x-ui-multi-ip ready" once the routes stand, and is safe to re-run.
func InstallScript() string { return installScript }

// SetPrimaryScript moves the main default route to $1, an IP already on the box,
// and prints the default routes it left.
func SetPrimaryScript() string { return setPrimaryScript }
