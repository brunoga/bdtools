#!/bin/bash
# record.sh LIBRARY WORK: for each film in LIBRARY (a directory a film,
# full side-by-side .mkv files), record into WORK what titletrain.py
# learns from: stereobench samples of the first 90 seconds, one picture a
# second (samples/), the same 90 seconds at 6 pictures a second, grey and
# 960x540 (dense/), and a minute ten minutes in (neg/). names.tsv maps a
# film's file name to its folder name. Films already recorded are
# skipped, and everything runs at low priority.
set -u
lib=$1 work=$2
mkdir -p "$work/samples" "$work/dense" "$work/neg"
cd "$work" || exit 1
: > names.tsv
ls "$lib" | while read -r d; do
  f=$(ls "$lib/$d/"*.mkv 2>/dev/null | grep -v -i bdtools | head -1)
  [ -z "$f" ] && continue
  b=$(basename "$f" .mkv) folder=$(echo "$d" | tr -c 'A-Za-z0-9\n' '_')
  printf '%s\t%s\n' "$b" "$folder" >> names.tsv
  if ! ls samples/"$b"@0s-* >/dev/null 2>&1; then
    echo "== $d"
    nice -n 15 stereobench -every 24 -samples samples "$f@0s+90s" </dev/null 2>&1 | tail -1
    rm -f samples/*.bht
  fi
  for kind in dense neg; do
    out="$kind/$folder"
    [ "$(ls "$out" 2>/dev/null | wc -l)" -ge 100 ] && continue
    mkdir -p "$out"
    at="-t 90" && [ $kind = neg ] && at="-ss 600 -t 60"
    nice -n 15 ffmpeg -nostdin -v error $at -i "$f" -vf "crop=iw/2:ih:0:0,scale=960:540,fps=6,format=gray" "$out/%04d.png"
  done
done
