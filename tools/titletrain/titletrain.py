"""titletrain.py: the classifier of text laid over the picture (titles,
credits, logos) against text written on things in a film, which
internal/textdet uses (overlay.json).

    titletrain.py build WORK MODEL      the lines of text record.sh recorded, measured, into WORK/feats.npz
    titletrain.py evaluate WORK         held out a film at a time: how many lines the discs lift it catches
    titletrain.py export WORK OUT.json  trained on every film, as textdet.LoadTrees reads it

MODEL is PP-OCRv4's text detector in ONNX (bdtools downloads it to the
user's cache as ch_PP-OCRv4_det_infer.onnx). It needs numpy, scipy,
Pillow, onnxruntime and scikit-learn. The measures are those of
internal/textdet (Regions, Features), and TestAgainstPrototype checks
that the two agree.
"""
import glob, json, os, re, struct, sys
import numpy as np
from PIL import Image
from scipy import ndimage as ndi

# the measures the classifier reads, by their index in feats.npz (the
# colour, 13, and the change half a second after, 16 and 17, are left
# out: the mid-film pictures are grey, and the lift runs before the
# picture after is there)
COLS = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 14, 15, 18, 19]


def read_bds(path):
    """a stereobench -samples picture: half-size Y, Cb, Cr, the network's depth and the disc's disparity"""
    b = open(path, 'rb').read()
    assert b[:4] == b'BDS1'
    w, h, top, bottom = struct.unpack('<4I', b[4:20])
    o, n = 20, w * h
    y = np.frombuffer(b, np.uint8, n, o).reshape(h, w)
    cb = np.frombuffer(b, np.uint8, n, o + n).reshape(h, w)
    cr = np.frombuffer(b, np.uint8, n, o + 2 * n).reshape(h, w)
    d = np.frombuffer(b, np.float32, n, o + 3 * n + 4 * n).reshape(h, w)
    return y, cb, cr, d


_session = None


def prob(model, y):
    """the detector's text probability map of a grey picture, at its size"""
    global _session
    import onnxruntime as ort
    if _session is None:
        o = ort.SessionOptions()
        o.intra_op_num_threads = int(os.environ.get('TITLETRAIN_THREADS', '4'))
        _session = ort.InferenceSession(model, o, providers=['CPUExecutionProvider'])
    h, w = y.shape
    x = np.zeros((1, 3, (h + 31) // 32 * 32, (w + 31) // 32 * 32), np.float32)
    v = y.astype(np.float32) / 255
    for c, (m, s) in enumerate(zip([0.485, 0.456, 0.406], [0.229, 0.224, 0.225])):
        x[0, c, :h, :w] = (v - m) / s
    return _session.run(None, {'x': x})[0][0, 0, :h, :w]


def regions(p):
    """textdet.Regions: bands of the map over 0.3, of 40 pixels or more, twice as wide as tall, grown"""
    lab, _ = ndi.label(p > 0.3)
    out = []
    for i, sl in enumerate(ndi.find_objects(lab)):
        if sl is None:
            continue
        y0, y1, x0, x1 = sl[0].start, sl[0].stop, sl[1].start, sl[1].stop
        h = max(y1 - y0, 1)
        if (lab[sl] == i + 1).sum() < 40 or (x1 - x0) < 2 * h:
            continue
        e = int(round(0.9 * h))
        out.append([max(0, y0 - e), min(p.shape[0], y1 + e), max(0, x0 - e // 2), min(p.shape[1], x1 + e // 2), h])
    return out


def features(y, pr, r, near, cb=None, cr=None):
    """textdet.Features, and the colour (13), which the classifier does not read"""
    y0, y1, x0, x1, h = r
    H, W = y.shape
    sub = y[y0:y1, x0:x1].astype(np.float32)
    dev = sub - ndi.uniform_filter(sub, size=2 * max(3, h // 2) + 1)
    stroke = np.abs(dev) > 25
    g = max(8, h)
    ring = np.zeros((H, W), bool)
    ring[max(0, y0 - g):y1 + g, max(0, x0 - g):x1 + g] = True
    ring[y0:y1, x0:x1] = False
    any_ = stroke.any()
    f = [h / H, (x1 - x0) / W, (x1 - x0) / max(h, 1), abs((x0 + x1) / 2 / W - 0.5), (y0 + y1) / 2 / H,
         stroke.mean(), float(sub[stroke].mean()) if any_ else 0, float(np.abs(dev[stroke]).mean()) if any_ else 0,
         float(np.sign(dev[stroke]).mean()) if any_ else 0,
         float(y[ring].mean()), float(y[ring].std()), float(sub.std()), float(pr[y0:y1, x0:x1].mean())]
    if cb is not None:
        sat = np.hypot(cb[y0:y1, x0:x1].astype(np.float32) - 128, cr[y0:y1, x0:x1].astype(np.float32) - 128)
        f.append(float(sat[stroke].mean()) if any_ else 0)
    else:
        f.append(np.nan)
    for a in near:  # half a second before, after, two seconds before
        if a is None:
            f += [np.nan] * 2
            continue
        f += [float(np.abs(y[y0:y1, x0:x1] - a[y0:y1, x0:x1]).mean()), float(np.abs(y[ring] - a[ring]).mean())]
    return f


def frame(d, i):
    p = f'{d}/{i:04d}.png'
    return np.asarray(Image.open(p)).astype(np.float32) if d and os.path.exists(p) else None


def build(work, model):
    folders = dict(l.rstrip('\n').split('\t') for l in open(os.path.join(work, 'names.tsv')))
    films = {}
    for p in sorted(glob.glob(os.path.join(work, 'samples', '*.bds'))):
        m = re.match(r'(.*)@0s-(\d+)\.bds$', os.path.basename(p))
        films.setdefault(m.group(1), []).append((int(m.group(2)), p))
    X, Y, G = [], [], []
    for name, pics in films.items():
        dense = os.path.join(work, 'dense', folders.get(name, '-'))
        for k, p in sorted(pics):
            y8, cb, cr, d = read_bds(p)
            y, ok = y8.astype(np.float32), ~np.isnan(d)
            i = round(k / 24 * 6) + 1
            near = [frame(dense, i - 3), frame(dense, i + 3), frame(dense, i - 12)]
            pr = prob(model, y8)
            for r in regions(pr):
                y0, y1, x0, x1, h = r
                # the label: the disc's disparity of the letters less that of the ring around them
                sub = y[y0:y1, x0:x1]
                stroke = (np.abs(sub - ndi.uniform_filter(sub, size=2 * max(3, h // 2) + 1)) > 25) & ok[y0:y1, x0:x1]
                g = max(8, h)
                ring = np.zeros_like(ok)
                ring[max(0, y0 - g):y1 + g, max(0, x0 - g):x1 + g] = True
                ring[y0:y1, x0:x1] = False
                ring &= ok
                if stroke.sum() < 30 or ring.sum() < 100:
                    continue
                X.append(features(y, pr, r, near, cb, cr))
                Y.append(float(np.median(d[y0:y1, x0:x1][stroke]) - np.median(d[ring])))
                G.append(name)
        print(name, len(X), flush=True)
    for dd in sorted(glob.glob(os.path.join(work, 'neg', '*'))):
        fs = sorted(glob.glob(dd + '/*.png'))
        for i in range(13, len(fs) - 3, 6):
            y = frame(dd, i)
            pr = prob(model, y.astype(np.uint8))
            near = [frame(dd, i - 3), frame(dd, i + 3), frame(dd, i - 12)]
            for r in regions(pr):
                X.append(features(y, pr, r, near))
                Y.append(-999)  # text ten minutes in: written on things, mostly
                G.append('neg:' + os.path.basename(dd))
        print(dd, len(X), flush=True)
    np.savez(os.path.join(work, 'feats.npz'), X=np.array(X, np.float32), Y=np.array(Y, np.float32), G=np.array(G))


def load(work):
    z = np.load(os.path.join(work, 'feats.npz'))
    X, Y, G = z['X'][:, COLS], z['Y'], z['G']
    key = lambda g: re.sub(r'[^a-z0-9]', '', (g[4:] if g.startswith('neg:') else g).lower())[:8]
    return X, Y, np.array([key(g) for g in G]), Y != -999


def model():
    from sklearn.ensemble import HistGradientBoostingClassifier
    return HistGradientBoostingClassifier(max_iter=200, learning_rate=0.05, max_leaf_nodes=15, class_weight='balanced', random_state=0)


def evaluate(work):
    X, Y, K, target = load(work)
    pred = np.full(len(X), np.nan)
    for k in sorted(set(K)):  # a film (its sequels with it) held out at a time
        t = K == k
        pred[t] = model().fit(X[~t], target[~t]).predict_proba(X[t])[:, 1]
    lifted, kept, scene = Y > 5, (Y != -999) & (np.abs(Y) <= 2), Y == -999
    for th in (0.5, 0.7, 0.85):
        p = pred > th
        print(f'{th}: lines the discs lift {p[lifted].mean():.0%}, opening lines they keep {p[kept].mean():.0%}, text ten minutes in {p[scene].mean():.1%}')


def export(work, out):
    X, _, _, target = load(work)
    m = model().fit(X, target)
    trees = []
    for (p,) in m._predictors:
        t = []
        for n in p.nodes:
            if n['is_leaf']:
                t.append({'leaf': True, 'v': float(n['value'])})
            else:
                t.append({'f': int(n['feature_idx']), 't': float(n['num_threshold']), 'nl': bool(n['missing_go_to_left']),
                          'l': int(n['left']), 'r': int(n['right'])})
        trees.append(t)
    # textdet's measures have no colour, so the ones after it are one place down
    cols = [c if c < 13 else c - 1 for c in COLS]
    json.dump({'base': float(m._baseline_prediction.ravel()[0]), 'cols': cols, 'trees': trees}, open(out, 'w'), separators=(',', ':'))


if __name__ == '__main__':
    cmd = sys.argv[1] if len(sys.argv) > 1 else ''
    if cmd == 'build' and len(sys.argv) == 4:
        build(sys.argv[2], sys.argv[3])
    elif cmd == 'evaluate' and len(sys.argv) == 3:
        evaluate(sys.argv[2])
    elif cmd == 'export' and len(sys.argv) == 4:
        export(sys.argv[2], sys.argv[3])
    else:
        sys.exit(__doc__)
