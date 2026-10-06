# Muxer fixtures

27 frames of the mvc-source pair, decoded side by side and encoded with
B-frames, for the Matroska muxer's frame-order tests:

```
x264 --crf 40 --bframes 3 --b-pyramid normal --keyint 12 --output bframes.264 clip.y4m
x265 --crf 40 --bframes 4 --keyint 12 --input clip.y4m --y4m --output bframes.265
```

The x265 stream has an open GOP: the CRA at the second keyframe is followed
in decode order by pictures shown before it.
