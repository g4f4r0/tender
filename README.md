# tender

Tends your machines: nightly whole-server restic backups, usage, and alerts, readable by AI agents over MCP.

```sh
CGO_ENABLED=0 go build -o tender ./cmd/tender
./tender hub --database ./hub.db --listen 127.0.0.1:8080
curl http://127.0.0.1:8080/v1/health
```
