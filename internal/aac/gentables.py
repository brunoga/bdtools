# Generates tables_gen.go from FFmpeg's libavcodec/aactab.c (release 7.1):
#   python3 gentables.py path/to/ffmpeg/libavcodec tables_gen.go
import re, sys
ref = sys.argv[1]
src_c = open(f"{ref}/aactab.c").read()
def array(name):
    m = re.search(re.escape(name) + r"\)?\s*\[[^=]*=\s*\{", src_c)
    if not m: raise SystemExit("no " + name)
    i = m.end(); depth = 1; j = i
    while depth:
        c = src_c[j]
        if c == '{': depth += 1
        elif c == '}': depth -= 1
        j += 1
    body = re.sub(r"/\*.*?\*/", "", src_c[i:j-1], flags=re.S)
    body = re.sub(r"//[^\n]*", "", body)
    return [int(x, 0) for x in re.findall(r"-?(?:0x[0-9a-fA-F]+|\d+)", body)]
def emit(name, typ, vals, cols=12):
    out = [f"var {name} = [{len(vals)}]{typ}{{"]
    for k in range(0, len(vals), cols):
        out.append("\t" + ", ".join(map(str, vals[k:k+cols])) + ",")
    out.append("}\n")
    return "\n".join(out)
out = """// Code generated from FFmpeg's libavcodec/aactab.c (release 7.1), LGPL
// 2.1 or later; see NOTICE. DO NOT EDIT.

package aac

"""
out += emit("scalefactorCode", "uint32", array("ff_aac_scalefactor_code"), 8)
out += emit("scalefactorBits", "uint8", array("ff_aac_scalefactor_bits"))
sizes = [81, 81, 81, 81, 81, 81, 64, 64, 169, 169, 289]
for b in range(1, 12):
    c, bt = array(f"codes{b}"), array(f"bits{b}")
    assert len(c) == sizes[b-1] and len(bt) == sizes[b-1], b
    out += emit(f"codes{b}", "uint16", c)
    out += emit(f"bits{b}", "uint8", bt)
out += "var spectralCodes = [11][]uint16{" + ", ".join(f"codes{b}[:]" for b in range(1, 12)) + "}\n\n"
out += "var spectralBits = [11][]uint8{" + ", ".join(f"bits{b}[:]" for b in range(1, 12)) + "}\n\n"
out += emit("swbOffset1024_48", "uint16", array("swb_offset_1024_48"))
open(sys.argv[2], "w").write(out)
