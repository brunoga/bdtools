// refdump: decodes an Annex B (optionally MVC) stream with edge264 and writes
// the cropped output planes of each view in display order, as a reference
// for testing the Go decoder.
//   refdump in.264 base.yuv [dep.yuv]
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "edge264.h"

static FILE *fb, *fd;
static long nframes;

// edge264 already offsets the plane pointers to the crop window.
static void write_view(FILE *f, const uint8_t *const s[3], const Edge264Frame *o) {
	for (int y = 0; y < o->height_Y; y++)
		fwrite(s[0] + y * o->stride_Y, 1, o->width_Y, f);
	for (int p = 1; p < 3; p++)
		for (int y = 0; y < o->height_C; y++)
			fwrite(s[p] + y * o->stride_C, 1, o->width_C, f);
}

static void drain(Edge264Decoder *d) {
	Edge264Frame o;
	while (!edge264_get_frame(d, &o, 0)) {
		write_view(fb, o.samples, &o);
		if (fd && o.samples_mvc[0])
			write_view(fd, o.samples_mvc, &o);
		nframes++;
	}
}

int main(int argc, char **argv) {
	if (argc < 3) { fprintf(stderr, "usage: refdump in.264 base.yuv [dep.yuv]\n"); return 2; }
	FILE *in = fopen(argv[1], "rb");
	if (!in) { perror(argv[1]); return 1; }
	fseek(in, 0, SEEK_END); long n = ftell(in); fseek(in, 0, SEEK_SET);
	uint8_t *buf = aligned_alloc(64, (n + 64 + 63) & ~63L);
	if (fread(buf, 1, n, in) != (size_t)n) return 1;
	memset(buf + n, 0, 64);
	fb = fopen(argv[2], "wb");
	fd = argc > 3 ? fopen(argv[3], "wb") : NULL;
	Edge264Decoder *d = edge264_alloc(1, NULL, NULL, 0, NULL, NULL, NULL);
	const uint8_t *end = buf + n;
	const uint8_t *nal = edge264_find_start_code(buf, end, 0);
	if (nal < end) nal += 3;
	int stuck = 0;
	while (nal < end) {
		const uint8_t *e = edge264_find_start_code(nal, end, 0);
		int res = edge264_decode_NAL(d, nal, e, NULL, NULL);
		long before = nframes;
		drain(d);
		if (res == ENOBUFS) {
			if (nframes == before && ++stuck > 64) { fprintf(stderr, "stuck\n"); break; }
			continue;
		}
		stuck = 0;
		if (res != 0 && res != ENOTSUP && res != ENODATA)
			fprintf(stderr, "decode_NAL: %d at offset %ld\n", res, (long)(nal - buf));
		nal = e + 3;
	}
	edge264_decode_NAL(d, end, end, NULL, NULL);
	drain(d);
	edge264_flush(d);
	drain(d);
	edge264_free(&d);
	fprintf(stderr, "%ld frames\n", nframes);
	return 0;
}
