#!/usr/bin/env python3
"""Converts ffmpeg's VC-1 table data (the standard's tables: codes and
lengths, scans, constants) into Go composite literals: tables.go.

    python3 gen_tables.py LIBAVCODEC/vc1_vlc_data.h,LIBAVCODEC/vc1data.c,\
        LIBAVCODEC/vc1acdata.h,LIBAVCODEC/msmpeg4_vc1_data.c tables.go
    gofmt -w tables.go
"""
import re
import sys

SRC = sys.argv[1]
OUT = sys.argv[2]

SYMS = {
    'TT_8X8': 0, 'TT_8X4_BOTTOM': 1, 'TT_8X4_TOP': 2, 'TT_8X4': 3,
    'TT_4X8_RIGHT': 4, 'TT_4X8_LEFT': 5, 'TT_4X8': 6, 'TT_4X4': 7,
    'MV_PMODE_1MV_HPEL_BILIN': 0, 'MV_PMODE_1MV': 1, 'MV_PMODE_1MV_HPEL': 2,
    'MV_PMODE_MIXED_MV': 3, 'MV_PMODE_INTENSITY_COMP': 4,
    'MV_PMODE_INTFR_1MV': 0, 'MV_PMODE_INTFR_2MV_FIELD': 1, 'MV_PMODE_INTFR_2MV': 2,
    'MV_PMODE_INTFR_4MV_FIELD': 3, 'MV_PMODE_INTFR_4MV': 4, 'MV_PMODE_INTFR_INTRA': 5,
    'AC_MODES': 8, 'WMV1_SCANTABLE_COUNT': 4,
}


def strip_comments(s):
    s = re.sub(r'/\*.*?\*/', '', s, flags=re.S)
    return re.sub(r'//[^\n]*', '', s)


def parse_braces(s, i):
    """Parses the initializer starting at s[i] == '{' into nested lists."""
    assert s[i] == '{'
    out, tok = [], ''
    i += 1
    while True:
        c = s[i]
        if c == '{':
            v, i = parse_braces(s, i)
            out.append(v)
            tok = ''
            continue
        if c in ',}':
            t = tok.strip()
            if t:
                out.append(value(t))
            tok = ''
            if c == '}':
                return out, i + 1
        else:
            tok += c
        i += 1


def value(t):
    if t in SYMS:
        return SYMS[t]
    return int(t, 0)


def decls(text):
    text = strip_comments(text)
    pat = re.compile(r'(?:static\s+)?const\s+(\w+)\s+(\w+)\s*((?:\[[^\]]*\]\s*)+)=\s*\{')
    for m in pat.finditer(text):
        dims = [d.strip() for d in re.findall(r'\[([^\]]*)\]', m.group(3))]
        dims = [SYMS.get(d, None) if not d.isdigit() else int(d) for d in dims]
        v, _ = parse_braces(text, m.end() - 1)
        yield m.group(2), dims, v


def shape(v):
    if not isinstance(v, list):
        return []
    sub = [shape(x) for x in v]
    inner = []
    for s in sub:
        for k, d in enumerate(s):
            if k >= len(inner):
                inner.append(d)
            else:
                inner[k] = max(inner[k], d)
    return [len(v)] + inner


def golit(v):
    if not isinstance(v, list):
        return str(v)
    if v and not isinstance(v[0], list):
        lines, line = [], []
        for x in v:
            line.append(str(x))
            if len(line) == 16:
                lines.append(', '.join(line) + ',')
                line = []
        if line:
            lines.append(', '.join(line) + ',')
        if len(lines) == 1:
            return '{' + lines[0].rstrip(',') + '}'
        return '{\n' + '\n'.join(lines) + '\n}'
    return '{\n' + '\n'.join(golit(x) + ',' for x in v) + '\n}'


def flat(v):
    if isinstance(v, list):
        for x in v:
            yield from flat(x)
    else:
        yield v


WANT = {
    # name in ffmpeg: Go name
    'vc1_imode_codes': 'imodeCodes', 'vc1_imode_bits': 'imodeBits',
    'vc1_norm2_codes': 'norm2Codes', 'vc1_norm2_bits': 'norm2Bits',
    'vc1_norm6_codes': 'norm6Codes', 'vc1_norm6_bits': 'norm6Bits',
    'vc1_4mv_block_pattern_codes': 'fourMVBPCodes', 'vc1_4mv_block_pattern_bits': 'fourMVBPBits',
    'vc1_2mv_block_pattern_codes': 'twoMVBPCodes', 'vc1_2mv_block_pattern_bits': 'twoMVBPBits',
    'vc1_intfr_4mv_mbmode_codes': 'intfr4MVModeCodes', 'vc1_intfr_4mv_mbmode_bits': 'intfr4MVModeBits',
    'vc1_intfr_non4mv_mbmode_codes': 'intfrNon4MVModeCodes', 'vc1_intfr_non4mv_mbmode_bits': 'intfrNon4MVModeBits',
    'vc1_if_mmv_mbmode_codes': 'ifMixedModeCodes', 'vc1_if_mmv_mbmode_bits': 'ifMixedModeBits',
    'vc1_if_1mv_mbmode_codes': 'if1MVModeCodes', 'vc1_if_1mv_mbmode_bits': 'if1MVModeBits',
    'vc1_1ref_mvdata_codes': 'mvdata1RefCodes', 'vc1_1ref_mvdata_bits': 'mvdata1RefBits',
    'vc1_2ref_mvdata_codes': 'mvdata2RefCodes', 'vc1_2ref_mvdata_bits': 'mvdata2RefBits',
    'vc1_cbpcy_p_codes': 'cbpcyPCodes', 'vc1_cbpcy_p_bits': 'cbpcyPBits',
    'vc1_icbpcy_p_codes': 'icbpcyCodes', 'vc1_icbpcy_p_bits': 'icbpcyBits',
    'vc1_ttmb_codes': 'ttmbCodes', 'vc1_ttmb_bits': 'ttmbBits',
    'vc1_ttblk_codes': 'ttblkCodes', 'vc1_ttblk_bits': 'ttblkBits',
    'vc1_subblkpat_codes': 'subblkpatCodes', 'vc1_subblkpat_bits': 'subblkpatBits',
    'vc1_mv_diff_codes': 'mvDiffCodes', 'vc1_mv_diff_bits': 'mvDiffBits',
    'vc1_ac_tables': 'acTables', 'ff_vc1_ac_sizes': 'acSizes',
    'ff_vc1_ttblk_to_tt': 'ttblkToTT', 'ff_vc1_ttfrm_to_tt': 'ttfrmToTT',
    'ff_vc1_mv_pmode_table': 'mvPModeTable', 'ff_vc1_mv_pmode_table2': 'mvPModeTable2',
    'ff_vc1_mbmode_intfrp': 'mbModeIntfrP', 'ff_vc1_pquant_table': 'pquantTable',
    'ff_wmv3_dc_scale_table': 'dcScaleTable',
    'ff_vc1_simple_progressive_4x4_zz': 'zz4x4Progressive',
    'ff_vc1_adv_progressive_8x4_zz': 'zz8x4Progressive',
    'ff_vc1_adv_progressive_4x8_zz': 'zz4x8Progressive',
    'ff_vc1_adv_interlaced_8x8_zz': 'zz8x8Interlaced',
    'ff_vc1_adv_interlaced_8x4_zz': 'zz8x4Interlaced',
    'ff_vc1_adv_interlaced_4x8_zz': 'zz4x8Interlaced',
    'ff_vc1_adv_interlaced_4x4_zz': 'zz4x4Interlaced',
    'ff_vc1_dqscale': 'dqScale',
    'ff_vc1_field_mvpred_scales': 'fieldMVPredScales',
    'ff_vc1_b_field_mvpred_scales': 'bFieldMVPredScales',
    'vc1_last_decode_table': 'acLastIndex', 'vc1_index_decode_table': 'acRunLevel',
    'vc1_delta_level_table': 'acDeltaLevel', 'vc1_last_delta_level_table': 'acLastDeltaLevel',
    'vc1_delta_run_table': 'acDeltaRun', 'vc1_last_delta_run_table': 'acLastDeltaRun',
    'ff_msmp4_mb_i_table': 'mbICBPTable', 'ff_msmp4_dc_tables': 'dcTables',
    'ff_wmv1_scantable': 'wmv1Scan',
}

found = {}
for path in SRC.split(','):
    with open(path) as f:
        for name, dims, v in decls(f.read()):
            if name in WANT and name not in found:
                found[name] = (dims, v)

missing = [n for n in WANT if n not in found]
if missing:
    sys.exit('missing: %s' % missing)

out = ['// Code generated by gen_tables.py from the standard\'s tables; DO NOT EDIT.', '',
       'package vc1', '']
for name, goname in WANT.items():
    dims, v = found[name]
    sh = shape(v)
    dims = [d if d is not None else s for d, s in zip(dims, sh)]
    vals = list(flat(v))
    lo, hi = min(vals), max(vals)
    if lo >= 0 and hi < 256:
        t = 'uint8'
    elif lo >= -32768 and hi < 32768:
        t = 'int16'
    else:
        t = 'int32'
    typ = ''.join('[%d]' % d for d in dims) + t
    out.append('var %s = %s%s' % (goname, typ, golit(v)))
    out.append('')

with open(OUT, 'w') as f:
    f.write('\n'.join(out))
