# M5 — console day

Offline drives a day of factory work through the console. The model router is `spike/m5/router.py`, in front of `spike/m1/model-stub`. Live and live-ci use the real model server and do not start that router.

`M5_ROUTER_PORT` sets the offline router port. When it is unset, `run.sh` picks a port that binds on both `0.0.0.0` and `127.0.0.1`. After the router starts, `GET /v1/models` on `127.0.0.1` and from a container via `host.docker.internal` must return the `m5-*` model list. Anything else stops with `router port <port> is answered by another process`.

```bash
./spike/m5/run_test.sh
```

Scratch is `spike/m5/.work/`, which is gitignored.
