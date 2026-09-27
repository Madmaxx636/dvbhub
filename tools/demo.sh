#!/usr/bin/env bash
# Rebuild and (re)start the demo server on the data left by tools/e2e.sh,
# with two live viewers so the dashboard shows activity. Ctrl-C stops it.
cd "$(dirname "$0")/.."
B=http://127.0.0.1:9980
[[ -f build/demo.pid ]] && kill "$(cat build/demo.pid)" 2>/dev/null
[[ -n "${1:-}" ]] && kill "$1" 2>/dev/null   # optional: pid of an older server
sleep 1
go build -o build/dvbhub ./cmd/dvbhub || exit 1
./build/dvbhub -data build/e2e/data -listen 127.0.0.1:9980 -virtual 2 &
PID=$!
echo $PID > build/demo.pid
trap 'kill $PID 2>/dev/null; kill 0' INT TERM EXIT
for i in $(seq 50); do curl -sf $B/discover.json >/dev/null && break; sleep 0.2; done
ids=$(curl -sf $B/api/channels | python3 -c "import json,sys; print(' '.join(c['id'] for c in json.load(sys.stdin) if not c['radio']))")
set -- $ids
(while kill -0 $PID 2>/dev/null; do curl -s --max-time 600 $B/stream/channel/$1 -o /dev/null; sleep 1; done) &
(while kill -0 $PID 2>/dev/null; do curl -s --max-time 600 "$B/stream/channel/$2?profile=cpu-cbr-3m" -o /dev/null; sleep 1; done) &
echo "Demo server running at $B (pid $PID). Press Ctrl-C to stop."
wait $PID
