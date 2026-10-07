# Cacao

Candy Server with WebUI

## Build

Requires Go 1.25 or newer and Node.js/npm for the frontend build.

```bash
# build a binary that runs natively
make

# build multiple platform binaries
make all
```

## Run

```bash
# loglevel=[info] listen=[:80] storage=[.]
cacao

# loglevel=[debug] listen=[127.0.0.1:8080] storage=[/var/lib/cacao]
cacao --loglevel=debug --listen=127.0.0.1:8080 --storage=/var/lib/cacao
```
