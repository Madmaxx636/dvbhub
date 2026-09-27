#!/usr/bin/env bash
# End-to-end test of dvbhub using virtual (file-backed) tuners.
# Output is also written to build/e2e.log. Pass "keep" to leave the server running.
cd "$(dirname "$0")/.."
exec > >(tee build/e2e.log) 2>&1
set -u
B=http://127.0.0.1:9980
T=build/e2e
PASS=0; FAIL=0
ok()   { echo "PASS: $*"; PASS=$((PASS+1)); }
bad()  { echo "FAIL: $*"; FAIL=$((FAIL+1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }
# j EXPR: evaluate a python expression over JSON on stdin; objects allow attribute access (d.name).
j() { python3 -c "
import json,sys
from types import SimpleNamespace as N
d=json.load(sys.stdin, object_hook=lambda o: N(**{k.replace('-','_'):v for k,v in o.items()}))
print($1)"; }
subs_on() { curl -sf $B/api/status | j "' '.join(t.key for t in d.tuners if any(s.name=='$1' for s in (t.subscriptions or [])))"; }

gofmt -w internal cmd tools
go vet ./... || exit 1
go build -o build/dvbhub ./cmd/dvbhub || exit 1
go build -o build/tsinject ./tools/tsinject || exit 1
rm -rf "$T"; mkdir -p "$T"

echo "== generating test streams"
FF="ffmpeg -hide_banner -loglevel error -y"
$FF -f lavfi -i testsrc2=size=1280x720:rate=25 -f lavfi -i sine=f=440:sample_rate=48000 \
    -f lavfi -i testsrc=size=720x576:rate=25 -f lavfi -i sine=f=660:sample_rate=48000 \
    -map 0:v -map 1:a -map 2:v -map 3:a -t 20 \
    -c:v:0 libx264 -preset ultrafast -g 25 -b:v:0 2M -c:a:0 aac -b:a 128k \
    -c:v:1 mpeg2video -g 12 -b:v:1 3M -c:a:1 mp2 -b:a:1 192k \
    -program title="News HD":program_num=101:st=0:st=1 -program title="Sport":program_num=102:st=2:st=3 \
    -mpegts_transport_stream_id 1 -mpegts_original_network_id 9018 -metadata service_provider=TestCo \
    -muxrate 8M -f mpegts $T/raw1.ts || exit 1
$FF -f lavfi -i testsrc2=size=1280x720:rate=25 -f lavfi -i sine=f=440:sample_rate=48000 -f lavfi -i sine=f=330:sample_rate=48000 \
    -map 0:v -map 1:a -map 2:a -t 20 \
    -c:v libx264 -preset ultrafast -g 25 -b:v 2M -c:a aac -b:a 128k \
    -program title="News HD":program_num=201:st=0:st=1 -program title="Radio One":program_num=202:st=2 \
    -mpegts_transport_stream_id 2 -mpegts_original_network_id 9018 -metadata service_provider=TestCo \
    -muxrate 6M -f mpegts $T/raw2.ts || exit 1
./build/tsinject -in $T/raw1.ts -out $T/mux1.ts -tsid 1 -lcn 101=1,102=4 -titles "101=Evening News,102=Football Live" -freq 506000
./build/tsinject -in $T/raw2.ts -out $T/mux2.ts -tsid 2 -lcn 201=101,202=700 -titles "201=Evening News,202=Morning Show" -freq 514000

echo "== starting server"
./build/dvbhub -data $T/data -listen 127.0.0.1:9980 -virtual 2 > $T/server.log 2>&1 &
PID=$!
[[ "${1:-}" == keep ]] || trap 'kill $PID 2>/dev/null' EXIT
for i in $(seq 50); do curl -sf $B/discover.json >/dev/null && break; sleep 0.2; done
check "server up" "curl -sf $B/discover.json >/dev/null"

NET=$(curl -sf -X POST $B/api/networks -d '{"name":"Test","type":"virtual","discoverMuxes":true}' | j 'd.id')
curl -sf -X POST $B/api/muxes -d "{\"networkId\":\"$NET\",\"file\":\"$PWD/$T/mux1.ts\",\"enabled\":true}" >/dev/null
curl -sf -X POST $B/api/muxes -d "{\"networkId\":\"$NET\",\"file\":\"$PWD/$T/mux2.ts\",\"enabled\":true}" >/dev/null
for i in $(seq 60); do
  S=$(curl -sf "$B/api/muxes?network=$NET" | j '",".join(m.scanStatus for m in d)')
  [[ "$S" == "ok,ok" || "$S" == *fail* ]] && break
  sleep 1
done
curl -sf "$B/api/muxes?network=$NET" | j '"\n".join(f"  mux {m.label}: {m.scanStatus} tsid={m.tsid} onid={m.onid} services={m.services}" for m in d)'
check "both muxes scanned" "[[ '$S' == 'ok,ok' ]]"
curl -sf $B/api/services | j '"\n".join(f"  svc {s.name!r:14} kind={s.kind:5} provider={s.provider!r} streams={[x.kind for x in s.streams]}" for s in d)'
check "4 services found" "[[ \$(curl -sf $B/api/services | j 'len(d)') == 4 ]]"
check "LCN from NIT (Sport=4)" "[[ \$(curl -sf $B/api/services | j '[getattr(s,\"lcn\",0) for s in d if s.name==\"Sport\"][0]') == 4 ]]"

echo "== mapping"
curl -sf -X POST $B/api/map -d '{"includeRadio":true,"skipScrambled":true,"mergeByName":true}'; echo
curl -sf $B/api/channels | j '"\n".join(f"  ch {c.number:<4} {c.name!r:12} services={c.serviceNames}" for c in d)'
check "3 channels (News HD merged)" "[[ \$(curl -sf $B/api/channels | j 'len(d)') == 3 ]]"
check "News HD has failover service" "[[ \$(curl -sf $B/api/channels | j '[len(c.services) for c in d if c.name==\"News HD\"][0]') == 2 ]]"
NEWS=$(curl -sf $B/api/channels | j '[c.id for c in d if c.name=="News HD"][0]')
SPORT=$(curl -sf $B/api/channels | j '[c.id for c in d if c.name=="Sport"][0]')

echo "== HDHomeRun / M3U"
curl -sf $B/discover.json; echo
curl -sf $B/lineup.json; echo
check "lineup has 3 entries" "[[ \$(curl -sf $B/lineup.json | j 'len(d)') == 3 ]]"
check "per-profile device id differs" "[[ \$(curl -sf $B/p/nvenc-h264-8m/discover.json | j 'd.DeviceID') != \$(curl -sf $B/discover.json | j 'd.DeviceID') ]]"
check "per-profile lineup URLs" "curl -sf $B/p/nvenc-h264-8m/lineup.json | grep -q '/p/nvenc-h264-8m/auto/v1'"
check "m3u lists 3 channels" "[[ \$(curl -sf $B/playlist.m3u | grep -c '^#EXTINF') == 3 ]]"

echo "== EPG from over-the-air EIT (idle grab after mapping)"
for i in $(seq 40); do
  N=$(curl -sf $B/xmltv.xml | grep -c '<programme')
  [[ $N -gt 40 ]] && break
  sleep 1
done
echo "  xmltv programmes: $N after ${i}s"
check "EIT guide collected for all 3 channels" "[[ $N -gt 40 ]]"
curl -sf $B/xmltv.xml | grep -m3 -E '<title>|<sub-title>|<category>'
check "epg grid" "[[ \$(curl -sf $B/api/epg/grid | j 'sum(len(c.events) for c in d.channels)') -gt 5 ]]"
sleep 6 # let the EPG grab release its tuners

echo "== passthrough stream (HDHomeRun URL)"
curl -s --max-time 6 $B/auto/v1 -o $T/ch1.ts
ls -l $T/ch1.ts | awk '{print "  "$5" bytes"}'
check "single program in output" "[[ \$(ffprobe -v error -show_entries program=program_id -of csv=p=0 $T/ch1.ts | grep -c .) == 1 ]]"
check "has h264 + aac" "ffprobe -v error -show_entries stream=codec_name -of csv=p=0 $T/ch1.ts | sort | tr '\n' ' ' | grep -q 'aac h264'"
sleep 5

echo "== signal drop with failover to backup service on another tuner"
curl -s --max-time 30 $B/stream/channel/$NEWS -o $T/drop.ts &
CPID=$!
sleep 4
TUNER=$(subs_on "News HD")
echo "  News HD streaming on $TUNER; dropping its signal for 25 s"
curl -sf -X POST "$B/api/tuners-drop/$TUNER?seconds=25" >/dev/null
S1=$(stat -c %s $T/drop.ts); sleep 5; S2=$(stat -c %s $T/drop.ts)
echo "  bytes during outage: $S1 -> $S2"
check "client kept alive during outage" "[[ $S2 -gt $S1 ]]"
curl -sf $B/api/signal | j '"\n".join(f"  signal {s.tuner}: state={s.state} quality={s.quality} strength={s.strengthPct:.0f}%" for s in d)'
sleep 8
curl -sf $B/api/status | j '"\n".join(f"  {t.key}: {t.state:9} {t.mux or None!s:22} subs={[(s.service, s.failovers) for s in (t.subscriptions or [])]}" for t in d.tuners)'
NEWT=$(subs_on "News HD")
check "failed over to other tuner ($TUNER -> $NEWT)" "[[ -n '$NEWT' && '$NEWT' != '$TUNER' ]]"
S3=$(stat -c %s $T/drop.ts); sleep 3; S4=$(stat -c %s $T/drop.ts)
echo "  bytes after failover: +$((S4-S3)) in 3 s"
check "real data flowing after failover" "[[ $((S4-S3)) -gt 400000 ]]"
wait $CPID
check "stream decodable after failover" "ffprobe -v error -show_entries stream=codec_name -of csv=p=0 $T/drop.ts | grep -q h264"
sleep 16  # let the simulated drop expire and sessions linger out

echo "== recovery on the same tuner (no backup service)"
curl -s --max-time 25 $B/stream/channel/$SPORT -o $T/sport.ts &
CPID=$!
sleep 4
T2=$(subs_on "Sport")
echo "  Sport on $T2; dropping signal for 8 s"
curl -sf -X POST "$B/api/tuners-drop/$T2?seconds=8" >/dev/null
sleep 3
ST1=$(curl -sf $B/api/status | j "[t.state for t in d.tuners if t.key=='$T2'][0]")
sleep 9
ST2=$(curl -sf $B/api/status | j "[t.state for t in d.tuners if t.key=='$T2'][0]")
echo "  during drop: $ST1, after: $ST2"
check "tuner reported no signal during drop" "[[ '$ST1' == 'nosignal' ]]"
check "tuner recovered to streaming" "[[ '$ST2' == 'streaming' ]]"
wait $CPID
check "Sport stream decodable (mpeg2 + mp2)" "ffprobe -v error -show_entries stream=codec_name -of csv=p=0 $T/sport.ts | sort | tr '\n' ' ' | grep -q 'mp2 mpeg2video'"
sleep 5

echo "== CPU transcode with CBR bitrate control"
curl -sf -X PUT $B/api/profiles/cpu-cbr-3m -d '{"name":"CPU CBR 3M","videoCodec":"libx264","preset":"veryfast","rateControl":"cbr","bitrate":3000,"height":480,"deinterlace":false,"audioCodec":"aac","audioBitrate":128,"audioChannels":2}' >/dev/null
check "profile saved" "curl -sf $B/api/profiles | grep -q cpu-cbr-3m"
curl -s --max-time 14 "$B/stream/channel/$SPORT?profile=cpu-cbr-3m" -o $T/tx.ts
ffprobe -v error -show_entries stream=codec_name,height -of compact=p=0 $T/tx.ts | sed 's/^/  /'
check "transcoded to h264 480p" "ffprobe -v error -select_streams v -show_entries stream=codec_name,height -of csv=p=0 $T/tx.ts | grep -q 'h264,480'"
BR=$(ffprobe -v error -show_entries format=bit_rate -of csv=p=0 $T/tx.ts)
echo "  measured output bitrate: $BR bit/s (target 3000k video + 128k audio)"
check "CBR bitrate within 25% of target" "[[ ${BR:-0} -gt 2350000 && ${BR:-0} -lt 3900000 ]]"
curl -s -o /dev/null -w '  invalid profile -> HTTP %{http_code}\n' -X PUT $B/api/profiles/bad -d '{"videoCodec":"rm -rf","audioCodec":"aac"}'

echo "== admin UI & API"
check "UI served" "curl -sf $B/ | grep -q dvbhub"
check "app.js served" "curl -sf $B/app.js | grep -q routes.dashboard"
check "system endpoint reports ffmpeg" "[[ \$(curl -sf $B/api/system | j 'd.transcode.ffmpegOk') == True ]]"
curl -sf $B/api/profiles | j '"\n".join(f"  {p.id}: {p.command[:170]}..." for p in d if p.command)' | head -2

echo "== ATSC mux: names and numbers from the PSIP VCT (no SDT)"
$FF -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine=f=440:sample_rate=48000 \
    -f lavfi -i testsrc=size=720x480:rate=30 -f lavfi -i sine=f=660:sample_rate=48000 \
    -map 0:v -map 1:a -map 2:v -map 3:a -t 20 \
    -c:v mpeg2video -g 15 -b:v 4M -c:a ac3 -b:a 192k \
    -program title="ignored1":program_num=3:st=0:st=1 -program title="ignored2":program_num=4:st=2:st=3 \
    -mpegts_transport_stream_id 1234 -muxrate 10M -f mpegts $T/raw3.ts || exit 1
./build/tsinject -in $T/raw3.ts -out $T/mux3.ts -atsc -tsid 1234 -vct "3=WTST-HD:3.1,4=Court:3.4"
ANET=$(curl -sf -X POST $B/api/networks -d '{"name":"ATSC test","type":"virtual"}' | j 'd.id')
curl -sf -X POST $B/api/muxes -d "{\"networkId\":\"$ANET\",\"file\":\"$PWD/$T/mux3.ts\",\"enabled\":true}" >/dev/null
for i in $(seq 40); do
  S=$(curl -sf "$B/api/muxes?network=$ANET" | j '",".join(m.scanStatus for m in d)')
  [[ "$S" == ok || "$S" == fail ]] && break
  sleep 1
done
echo "  scan: $S after ${i}s"
check "ATSC mux scanned" "[[ '$S' == ok ]]"
check "ATSC scan finished without waiting out the SDT/NIT timeout" "[[ $i -lt 12 ]]"
curl -sf $B/api/services | j '"\n".join(f"  svc {s.name!r:10} {s.lcn}.{s.minor} kind={s.kind}" for s in d if s.sid in (3,4))'
check "VCT short names used" "[[ \$(curl -sf $B/api/services | j '\",\".join(sorted(s.name for s in d if s.sid in (3,4)))') == 'Court,WTST-HD' ]]"
curl -sf -X POST $B/api/map -d '{"includeRadio":false,"skipScrambled":true,"mergeByName":true}' >/dev/null
check "channel numbered 3.1 in lineup" "curl -sf $B/lineup.json | j '[e.GuideNumber for e in d]' | grep -q \"'3.1'\""
check "3.4 streams via /auto/v3.4" "[[ \$(curl -s --max-time 4 $B/auto/v3.4 | wc -c) -gt 100000 ]]"
for i in $(seq 40); do
  N=$(curl -sf $B/xmltv.xml | grep -c 'WTST-HD Show')
  [[ $N -gt 10 ]] && break
  sleep 1
done
echo "  ATSC guide programmes for 3.1: $N after ${i}s"
check "ATSC PSIP guide collected (EIT)" "[[ $N -gt 10 ]]"
check "ATSC guide descriptions (ETT)" "curl -sf $B/xmltv.xml | grep -q 'About WTST-HD show'"
check "ATSC guide times are UTC-correct" "[[ \$(curl -sf $B/api/epg/grid | j 'min(abs((__import__(\"datetime\").datetime.fromisoformat(e.start.replace(\"Z\",\"+00:00\")).timestamp()) - __import__(\"time\").time()) for c in d.channels if c.name==\"WTST-HD\" for e in c.events) < 3700') == True ]]"

echo "== dead stream frees its tuner (no signal for over 60 s)"
curl -s --max-time 90 $B/auto/v3.1 -o /dev/null &
CPID=$!
sleep 4
DT=$(subs_on "WTST-HD")
echo "  WTST-HD on $DT; dropping its signal for 80 s"
curl -sf -X POST "$B/api/tuners-drop/$DT?seconds=80" >/dev/null
sleep 72
LEFT=$(subs_on "WTST-HD")
echo "  tuners still holding WTST-HD after 72 s without signal: ${LEFT:-none}"
check "dead session ended and released its tuner" "[[ -z '$LEFT' ]] && grep -q 'ending session' $T/server.log"
check "client connection was closed" "! kill -0 $CPID 2>/dev/null"
kill $CPID 2>/dev/null; wait $CPID 2>/dev/null

echo "== server log"
grep -vE 'listening|admin UI|HDHomeRun:|M3U / XMLTV' $T/server.log | sed 's/^/  /'
echo
echo "RESULT: $PASS passed, $FAIL failed"
[[ "${1:-}" == keep ]] || exit $((FAIL > 0))

if [[ "${1:-}" == keep ]]; then
  # Demo mode: leave the server up with two live viewers so the dashboard has something to show.
  (while kill -0 $PID 2>/dev/null; do curl -s --max-time 600 $B/stream/channel/$NEWS -o /dev/null; done) &
  (while kill -0 $PID 2>/dev/null; do curl -s --max-time 600 "$B/stream/channel/$SPORT?profile=cpu-cbr-3m" -o /dev/null; done) &
  echo "Demo server running at $B (pid $PID). Press Ctrl-C to stop."
  trap 'kill $PID 2>/dev/null; kill 0' INT TERM
  wait $PID
fi
