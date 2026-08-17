# Recording a demo

Maintainer runbook for producing the `docs/demo.gif` referenced from
`README.md`. Contributors do not need to follow this — it's a
one-shot artifact regenerated only when the headline workflow visibly
changes.

The README intentionally **does not commit** a placeholder gif;
the link to `docs/demo.gif` resolves to 404 until a real recording
lands. That is preferable to bundling a stale recording for every
checkout.

---

## Tooling

- [`asciinema`](https://asciinema.org/docs/installation) records the
  terminal session into a self-contained `.cast` file.
- [`agg`](https://github.com/asciinema/agg) (asciinema-gif-generator)
  renders the cast to a GIF.

```sh
brew install asciinema agg              # macOS
# or
cargo install --locked agg              # cross-platform alternative
```

---

## Setup before recording

1. **Use a test account.** See [SECURITY.md → Ban-risk
   warning](SECURITY.md#ban-risk-warning). Live messages from a primary
   account would leak into the GIF and live on the internet forever.
2. **Pre-populate the test account.** Make sure there is at least one
   chat with searchable history, one chat with a media attachment for
   the download step, and one user/channel that you can search by
   username (`from:@…`).
3. **Pick a wide-enough terminal.** 120×30 renders cleanly on the README;
   anything narrower clips the help overlay.
4. **Set a clean prompt.** A minimal prompt (`%~ %#` or `$ `) keeps the
   recording legible. Avoid prompts that show the current branch or
   k8s context — the GIF will outlive both.
5. **Pre-export credentials.** The recording should not show the
   `LAZYTG_API_*` exports.

---

## Scenario

Aim for ≤ 30 seconds rendered. The reader needs to see the headline:
"open the TUI → search → result → done."

Suggested script (read each line, run, wait two beats, continue):

```text
1. lazytg                          # open the TUI
2. <Down/Down/Enter>               # pick a chat from the list
3. <Tab>                           # focus input
4. type: "hello from lazytg"
5. <Enter>                         # send (optimistic render lands instantly)
6. </>                              # open search overlay
7. type: "тест"                    # show Unicode + p95 ≈ 47 ms
8. <Down/Enter>                    # jump to a result
9. <Ctrl+D>                        # download last media in thread
10. <?>                            # toggle help overlay (cinematic close)
11. <Ctrl+Q>                       # quit
```

If you want to highlight multi-account or `--polling`, record a
separate cast — keep the headline GIF tight.

---

## Recording

```sh
asciinema rec -c "lazytg" --idle-time-limit 1.5 docs/demo.cast
# Hit Ctrl+D (or Ctrl+Q in the TUI) when the script is done.
```

`-c "lazytg"` runs the binary directly in the recording subshell,
skipping prompt clutter. `--idle-time-limit 1.5` collapses pauses
longer than 1.5 s to 1.5 s — your typing rhythm stays human, but
thinking time doesn't bloat the GIF.

Re-record on any mistake. The cast file is small; iterate.

---

## Rendering to GIF

```sh
agg --speed 1.5 \
    --theme asciinema \
    --font-family "JetBrains Mono,Menlo,monospace" \
    --cols 120 --rows 30 \
    docs/demo.cast docs/demo.gif
```

`--speed 1.5` shaves a third off without making typing look frantic.
Pick a theme that matches the screenshots in the README; `asciinema`
and `dracula` both look good against GitHub's light/dark UIs.

---

## Sanity checks before committing

- File size below ~3 MB. GitHub renders inline gifs faithfully up to
  about that ceiling; bigger files load lazily and break the
  scroll-on-arrival effect.
- `file docs/demo.gif` says `GIF image data, ...`.
- Open the gif in a browser and confirm no message text from a
  primary account leaked. **If anything sensitive shows up,
  re-record.**
- Drop the cast file into `docs/demo.cast` only if you want others
  to be able to regenerate the GIF; otherwise it's safe to discard
  to keep the repo small.

---

## Updating the README

The README references the GIF as:

```markdown
![demo](docs/demo.gif)
```

No further changes needed once the file is in place — the placeholder
becomes live the moment the file is committed.

When the flow visibly changes (e.g. a new headline keybinding lands
in v0.2), regenerate the GIF and update the alt text.
