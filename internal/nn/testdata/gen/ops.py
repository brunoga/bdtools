# Writes ops.pth: random inputs and parameters for each op the Go package
# has, and PyTorch's outputs, for the tests to compare against.
#
#   python ops.py ../ops.pth
import sys, collections, math
import torch, torch.nn.functional as F

torch.manual_seed(1)
t = collections.OrderedDict()
r = lambda *s: torch.randn(*s)

x, w, b = r(5, 7), r(9, 7), r(9)
t['linear.x'], t['linear.w'], t['linear.b'], t['linear.y'] = x, w, b, F.linear(x, w, b)

for name, (c, h, wd, o, k, st, pad) in {
    'conv3': (3, 9, 11, 4, 3, 1, 1), 'conv3s2': (4, 10, 9, 5, 3, 2, 1),
    'conv1': (6, 5, 7, 3, 1, 1, 0), 'patch': (3, 28, 42, 8, 14, 14, 0)}.items():
    x, w, b = r(c, h, wd), r(o, c, k, k), r(o)
    t[name+'.x'], t[name+'.w'], t[name+'.b'] = x, w, b
    t[name+'.y'] = F.conv2d(x[None], w, b, stride=st, padding=pad)[0]

for name, (c, h, wd, o, k) in {'convt4': (3, 4, 5, 2, 4), 'convt2': (4, 3, 3, 5, 2)}.items():
    x, w, b = r(c, h, wd), r(c, o, k, k), r(o)
    t[name+'.x'], t[name+'.w'], t[name+'.b'] = x, w, b
    t[name+'.y'] = F.conv_transpose2d(x[None], w, b, stride=k)[0]

x, w, b = r(6, 16) * 3 + 1, r(16), r(16)
t['ln.x'], t['ln.w'], t['ln.b'], t['ln.y'] = x, w, b, F.layer_norm(x, (16,), w, b, 1e-6)

x, w, b = r(8, 3, 5) * 2 - 1, r(8), r(8)
t['gn.x'], t['gn.w'], t['gn.b'], t['gn.y'] = x, w, b, F.group_norm(x[None], 4, w, b, 1e-6)[0]

x = r(4, 9) * 3
t['gelu.x'], t['gelu.y'] = x, F.gelu(x)

q, k, v = r(7, 16), r(11, 16), r(11, 16)
heads = 4
def attn(q, k, v):
    qh = q.reshape(-1, heads, 4).permute(1, 0, 2)
    kh = k.reshape(-1, heads, 4).permute(1, 0, 2)
    vh = v.reshape(-1, heads, 4).permute(1, 0, 2)
    p = (qh @ kh.transpose(1, 2) * 4 ** -0.5).softmax(-1)
    return (p @ vh).permute(1, 0, 2).reshape(-1, 16)
t['attn.q'], t['attn.k'], t['attn.v'], t['attn.y'] = q, k, v, attn(q, k, v)

x = r(3, 4, 6)
t['bil.x'] = x
t['bil.up'] = F.interpolate(x[None], size=(9, 13), mode='bilinear', align_corners=True)[0]
t['bil.down'] = F.interpolate(x[None], size=(3, 2), mode='bilinear', align_corners=True)[0]

x = r(5, 6, 6)
sy, sx = (4 + 0.1) / 6, (9 + 0.1) / 6
t['bic.x'] = x
t['bic.scale'] = torch.tensor([sy, sx], dtype=torch.float64).float()
t['bic.y'] = F.interpolate(x[None], scale_factor=(sy, sx), mode='bicubic', antialias=False)[0]

torch.save(t, sys.argv[1])
