# Generates tables_gen.go from FFmpeg's libavcodec sources (release 7.1):
#   python3 gentables.py path/to/ffmpeg/libavcodec tables_gen.go
# (dcadata.c, dcahuff.c and dca_sample_rate_tab.h are what it reads.)
import re, sys
ref = sys.argv[1]
def array(file, name):
    s = open(f"{ref}/{file}").read()
    m = re.search(re.escape(name) + r"\)?\s*(\[[^=]*)?=\s*\{", s)
    if not m: raise SystemExit("no " + name)
    i = m.end(); depth = 1; j = i
    while depth:
        c = s[j]
        if c == '{': depth += 1
        elif c == '}': depth -= 1
        j += 1
    body = s[i:j-1]
    body = re.sub(r"/\*.*?\*/", "", body, flags=re.S)
    body = re.sub(r"//[^\n]*", "", body)
    return [int(x, 0) for x in re.findall(r"-?(?:0x[0-9a-fA-F]+|\d+)", body)]
def emit(name, typ, vals, cols, dims=None):
    out = []
    if dims:
        out.append(f"var {name} = [{dims[0]}][{dims[1]}]{typ}{{")
        for r in range(dims[0]):
            row = vals[r*dims[1]:(r+1)*dims[1]]
            out.append("\t{" + ", ".join(map(str,row)) + "},")
    else:
        out.append(f"var {name} = [{len(vals)}]{typ}{{")
        for k in range(0, len(vals), cols):
            out.append("\t" + ", ".join(map(str, vals[k:k+cols])) + ",")
    out.append("}\n")
    return "\n".join(out)
hdr = """// Code generated from FFmpeg's libavcodec (dcadata.c, dcahuff.c,
// dca_sample_rate_tab.h, release 7.1), LGPL 2.1 or later; see NOTICE.
// DO NOT EDIT.

package dts

"""
src = hdr
src += emit("sampleRates", "uint32", array("dca_sample_rate_tab.h", "ff_dca_sample_rates"), 16)
src += emit("bitRates", "uint32", array("dcadata.c", "ff_dca_bit_rates"), 8)
src += emit("channelCount", "uint8", array("dcadata.c", "ff_dca_channels"), 16)
src += emit("quantIndexSelBits", "uint8", array("dcadata.c", "ff_dca_quant_index_sel_nbits"), 16)
src += emit("quantIndexGroupSize", "uint8", array("dcadata.c", "ff_dca_quant_index_group_size"), 16)
src += emit("adpcmVB", "int16", array("dcadata.c", "ff_dca_adpcm_vb"), 4, (4096, 4))
src += emit("scaleFactorQuant6", "int32", array("dcadata.c", "ff_dca_scale_factor_quant6"), 8)
src += emit("scaleFactorQuant7", "int32", array("dcadata.c", "ff_dca_scale_factor_quant7"), 8)
src += emit("jointScaleFactors", "int32", array("dcadata.c", "ff_dca_joint_scale_factors"), 8)
src += emit("scaleFactorAdj", "int32", array("dcadata.c", "ff_dca_scale_factor_adj"), 8)
src += emit("quantLevels", "int32", array("dcadata.c", "ff_dca_quant_levels"), 8)
src += emit("lossyQuant", "int32", array("dcadata.c", "ff_dca_lossy_quant"), 8)
src += emit("losslessQuant", "int32", array("dcadata.c", "ff_dca_lossless_quant"), 8)
src += emit("highFreqVQ", "int8", array("dcadata.c", "ff_dca_high_freq_vq"), 32, (1024, 32))
src += emit("fir32Perfect", "int32", array("dcadata.c", "ff_dca_fir_32bands_perfect_fixed"), 8)
src += emit("fir32Nonperfect", "int32", array("dcadata.c", "ff_dca_fir_32bands_nonperfect_fixed"), 8)
src += emit("lfeFir64", "int32", array("dcadata.c", "ff_dca_lfe_fir_64_fixed"), 8)
src += emit("bitallocSizes", "int", array("dcahuff.c", "ff_dca_bitalloc_sizes"), 16)
src += emit("bitallocOffsets", "int", array("dcahuff.c", "ff_dca_bitalloc_offsets"), 16)
# The codebooks' (symbol, length) pairs, in canonical order, up to the
# transition mode books (the LBR ones after are not the core's).
pairs = array("dcahuff.c", "ff_dca_vlc_src_tables")
cbsizes = []
sizes = [3,5,7,9,13,17,25,33,65,129]
groups = [1,3,3,3,3,7,7,7,7,7]
for sz, g in zip(sizes, groups):
    cbsizes += [sz]*g
cbsizes += [12]*5 + [129]*5 + [4]*4
n = sum(cbsizes)
src += emit("vlcPairs", "uint8", [v & 0xff for v in pairs[:2*n]], 16)
open(sys.argv[2], "w").write(src)
print("pairs", n, "of", len(pairs)//2)
