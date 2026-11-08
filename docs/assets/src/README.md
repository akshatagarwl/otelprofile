# Diagrams

The animated SVGs in `docs/assets` are generated, not drawn by hand:
rough strokes come from [roughjs](https://roughjs.com) (MIT) and text is
converted to outlines of [Shantell Sans](https://github.com/arrowtype/shantell-sans)
(SIL Open Font License 1.1), so the files need no fonts or scripts and animate
inside an `<img>` with CSS only. Viewers that ask for reduced motion see a
still frame.

```sh
cd docs/assets/src
npm install
npm run build
```

The numbers in `overhead.svg` are round 13 of `ROUNDS.md` (linux/arm64
container). The screenshots (`*.png`) are captures of `examples/demo`
sending to a local sigiro and to Grafana Pyroscope 2.4.3.
