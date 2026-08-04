#!/usr/bin/env bash
#
# Render memory.sh's CSV as Markdown for a GitHub job summary.
#
# Emits a table first and the charts after it. That order is deliberate: the
# table is the record, the charts are the reading aid. GitHub renders mermaid in
# job summaries, but `xychart-beta` needs a recent enough Mermaid, and a
# renderer that does not know the directive shows the block as plain text — so
# the numbers must already be on the page by then.
#
# One chart per operation, single series. Mermaid's xychart has no legend, so a
# multi-series chart would be unreadable; a chart titled with its operation
# needs no legend at all.
#
# Usage: scripts/benchmark/render-memory.sh benchmark-results/memory.csv

set -euo pipefail

CSV=${1:-benchmark-results/memory.csv}

if [ ! -f "$CSV" ]; then
    echo "Error: $CSV not found" >&2
    exit 1
fi

operations=$(awk -F, 'NR > 1 { print $1 }' "$CSV" | awk '!seen[$0]++')
sizes=$(awk -F, 'NR > 1 { print $2 }' "$CSV" | sort -n | awk '!seen[$0]++')

# ---------------------------------------------------------------------------
# Table
# ---------------------------------------------------------------------------

printf '## Peak memory vs repository size\n\n'
printf 'Peak resident set size of the real binary, per operation, at each tree size.\n'
printf 'An operation whose row stays flat holds a working set; one that tracks the\n'
printf 'file count holds a per-entry structure and will eventually meet a repository\n'
printf 'it cannot open.\n\n'

printf '| Operation |'
for s in $sizes; do printf ' %s files |' "$s"; done
printf ' Growth |\n|---|'
for _ in $sizes; do printf '%s' '---:|'; done
printf '%s\n' '---:|'

for op in $operations; do
    printf '| `%s` |' "$op"
    first=""
    last=""
    for s in $sizes; do
        mb=$(awk -F, -v o="$op" -v f="$s" '$1 == o && $2 == f { print $3 }' "$CSV")
        [ -n "$mb" ] || mb="-"
        [ -n "$first" ] || first=$mb
        last=$mb
        printf ' %s MB |' "$mb"
    done
    if [ "$first" != "-" ] && [ "$last" != "-" ] && [ "$first" != "0" ]; then
        printf ' %sx |\n' "$(echo "scale=2; $last / $first" | bc)"
    else
        printf ' - |\n'
    fi
done

printf '\nWall time is recorded in the CSV artifact; this table is memory only, since\n'
printf 'timing on a shared runner is too noisy to read as a trend.\n'

# ---------------------------------------------------------------------------
# Charts
# ---------------------------------------------------------------------------

printf '\n### Charts\n'

# y-axis is shared across every chart so the panels are visually comparable —
# a per-chart axis would make a flat operation and a climbing one look alike.
ymax=$(awk -F, 'NR > 1 { if ($3 + 0 > m) m = $3 + 0 } END { printf "%d", (m * 1.15) + 1 }' "$CSV")

xaxis=$(printf '%s, ' $sizes | sed 's/, $//')

for op in $operations; do
    series=$(for s in $sizes; do
        awk -F, -v o="$op" -v f="$s" '$1 == o && $2 == f { printf "%s, ", $3 }' "$CSV"
    done | sed 's/, $//')

    printf '\n```mermaid\n'
    printf 'xychart-beta\n'
    printf '    title "%s — peak RSS (MB)"\n' "$op"
    printf '    x-axis "files in tree" [%s]\n' "$xaxis"
    printf '    y-axis "peak RSS (MB)" 0 --> %s\n' "$ymax"
    printf '    line [%s]\n' "$series"
    printf '```\n'
done
