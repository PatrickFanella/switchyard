# Phoenix Evaluations

## Bootstrap managed objects

```bash
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006 --verify
```

## Run smoke eval

```bash
python3 scripts/phoenix_eval_smoke.py --base-url http://10.0.0.200:6006 --broker-url http://127.0.0.1:11434
```

The smoke eval writes a code evaluation to the Phoenix experiment for `smoke-tests`.
