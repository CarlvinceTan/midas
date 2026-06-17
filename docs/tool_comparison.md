# Cross-tool browser-automation comparison

midas vs playwright-cli vs Vercel agent-browser, identical fixtures, each scenario verified through the tool's own `eval`.

Legend: PASS = outcome verified · FAIL = ran but wrong result · UNSUP = command not in that CLI · ERR = nav/eval/step error.

| scenario | midas | midas-daemon | playwright | agent-browser |
|---|---|---|---|---|
| nav-title | PASS | PASS | PASS | PASS |
| click | PASS | PASS | PASS | PASS |
| fill | PASS | PASS | PASS | PASS |
| form-submit | PASS | PASS | PASS | PASS |
| dblclick | PASS | PASS | PASS | PASS |
| checkbox-check | PASS | PASS | PASS | PASS |
| checkbox-uncheck | PASS | PASS | PASS | PASS |
| radio | PASS | PASS | PASS | PASS |
| select-by-value | PASS | PASS | PASS | PASS |
| select-by-label | PASS | PASS | PASS | PASS |
| hover | PASS | PASS | PASS | PASS |
| shadow-click | PASS | PASS | PASS | FAIL(click) |
| dynamic-wait-click | PASS | PASS | FAIL | FAIL(click) |
| scroll-into-view-click | PASS | PASS | PASS | FAIL |
| disabled-then-enabled | PASS | PASS | PASS | FAIL |
| overlay-clears | PASS | PASS | PASS | FAIL |
| keycombo-select-all | PASS | PASS | PASS | PASS |
| drag-drop | PASS | PASS | PASS | PASS |
| contenteditable-fill | PASS | PASS | PASS | PASS |

## Summary

| tool | PASS | FAIL | UNSUP | ERR | total time |
|---|---|---|---|---|---|
| midas | 19 | 0 | 0 | 0 | 4.988s |
| midas-daemon | 19 | 0 | 0 | 0 | 3.381s |
| playwright | 18 | 1 | 0 | 0 | 33.584s |
| agent-browser | 14 | 5 | 0 | 0 | 9.571s |

## Scenarios where the tools diverge

- disabled-then-enabled: midas=PASS, midas-daemon=PASS, playwright=PASS, agent-browser=FAIL
- dynamic-wait-click: midas=PASS, midas-daemon=PASS, playwright=FAIL, agent-browser=FAIL(click)
- overlay-clears: midas=PASS, midas-daemon=PASS, playwright=PASS, agent-browser=FAIL
- scroll-into-view-click: midas=PASS, midas-daemon=PASS, playwright=PASS, agent-browser=FAIL
- shadow-click: midas=PASS, midas-daemon=PASS, playwright=PASS, agent-browser=FAIL(click)
