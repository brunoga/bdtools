# Muxer fixtures

27 frames of the mvc-source pair, decoded side by side and encoded with
B-frames, for the Matroska muxer's frame-order tests:

```
x264 --crf 40 --bframes 3 --b-pyramid normal --keyint 12 --output bframes.264 clip.y4m
x265 --crf 40 --bframes 4 --keyint 12 --input clip.y4m --y4m --output bframes.265
```

The x265 stream has an open GOP: the CRA at the second keyframe is followed
in decode order by pictures shown before it.

`av1.obu` is the same 27 frames in AV1, as a low-overhead OBU stream (what
the GPU encoders write), keyframes every 12:

```
ffmpeg -stream_loop 2 -i clip.y4m -frames:v 27 -c:v libsvtav1 -crf 63 -g 12 \
    -preset 8 -svtav1-params enable-overlays=0 -f obu av1.obu
```

SVT-AV1 hides frames for its random-access structure, so its temporal units
carry several frame OBUs of which one is shown.
