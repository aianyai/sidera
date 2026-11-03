# testdata

Produced by the real tool, `minisign 0.12`, with two throwaway keys that
protect nothing. Neither secret key was kept; only the public keys are here.

```sh
minisign -G -W -p test.pub -s test.key      # likewise other.pub / other.key
printf 'a1b2  sidera_0.12.0_linux_amd64.tar.gz\nc3d4  sidera_0.12.0_windows_amd64.zip\n' > message.txt
minisign -S -s test.key  -m message.txt -t 'sidera 0.12.0 update-from 0.8.0' -x message.txt.minisig
minisign -S -s test.key  -m message.txt -t 'sidera 0.12.0 update-from 0.8.0' -x legacy.minisig -l
minisign -S -s other.key -m message.txt -t 'sidera 0.12.0 update-from 0.8.0' -x otherkey.minisig
```

| File | What it is |
| ---- | ---------- |
| `test.pub`, `other.pub` | The two public keys (`.pub` files, key ids `57478AD34CBC6FF4` and `BAC0B87022F9F43C`) |
| `message.txt` | The signed message |
| `message.txt.minisig` | Prehashed (`ED`) signature of `message.txt` by `test`, the default `minisign -S` output |
| `legacy.minisig` | The same, made with `-l`: legacy (`Ed`) format |
| `otherkey.minisig` | Prehashed signature of `message.txt` by `other` |
