# Blu-ray 3D fixtures

Synthetic, made here: the MVC pair is mvc-source's (`../mvc-source`), the
audio is generated sine tones (`src/`), and tsMuxeR 2.7.0 muxed them as a
Blu-ray:

```
MUXOPT --blu-ray --no-pcr-on-video-pid --new-audio-pes --vbr --vbv-len=500
V_MPEG4/ISO/AVC, "mvc_base.264", fps=23.976, insertSEI, contSPS
V_MPEG4/ISO/MVC, "mvc_dependent.mvc", fps=23.976, insertSEI, contSPS
A_AC3, "a.ac3", lang=eng
A_LPCM, "b.wav", lang=fra
A_AC3, "c.eac3", lang=deu
```

- `folder/` is the folder output: the base and dependent views as two
  `.m2ts` files, no SSIF (the BACKUP copy is left out).
- `disc.iso` is the image output: a UDF 2.50 image whose `STREAM/SSIF`
  interleaves the two views, as a pressed disc does.
- `src/d.dts` and `src/e.thd` are ffmpeg's DTS and TrueHD encoders' output,
  for the stream header parsers.
