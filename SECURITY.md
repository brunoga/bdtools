# Security Policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Use [GitHub private vulnerability reporting](https://github.com/brunoga/mvc/security/advisories/new) to report the issue confidentially. You will receive an acknowledgement within 48 hours and a resolution timeline within 7 days.

Please include:

- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept
- Any suggested fixes if you have them

## Scope

This repository is a video decoder and a command-line conversion tool, both
of which read untrusted input: a video stream is attacker-controlled data.

- **The decoder** is pure Go and memory-safe; a malformed stream must at
  worst produce wrong pictures or an error, never a crash. It is fuzzed
  (`FuzzDecode`) and recovers from panics in slice decoding. A reproducible
  panic, hang, or unbounded memory growth on any input is a bug we want to
  hear about.
- **mvctools** runs external programs (tsMuxeR, x264/x265, ffmpeg, mkvmerge)
  with arguments built from file paths and the probe output; nothing is passed
  through a shell. It reads disc images in place and writes only under the
  output and temp directories it is given.
