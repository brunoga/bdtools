# Reference outputs of Video Depth Anything's streaming inference, for the
# Go implementation (internal/depth) to match.
#
#   python vda.py tiny OUT_WEIGHTS OUT_IO        a small randomly initialised
#                                                model, 14 frames (CI fixtures)
#   python vda.py real WEIGHTS FRAMES_DIR OUT_IO [vits|vitb|vitl]
#                                                the published weights on
#                                                frames (PPM files)
#
# VDA is the Video-Depth-Anything repository's directory, on sys.path.
import sys, os, collections, functools, glob
sys.path.insert(0, os.environ['VDA'])
import numpy as np, torch, torch.nn.functional as F
from video_depth_anything.video_depth_stream import VideoDepthAnything, INFER_LEN
from video_depth_anything.dinov2 import DinoVisionTransformer
from video_depth_anything.dinov2_layers import MemEffAttention, NestedTensorBlock as Block
from video_depth_anything.dpt_temporal import DPTHeadTemporal

def stream(model, inputs):
    """infer_video_depth_one's loop, on preprocessed [3, H, W] inputs: the
    depth at the network's size after its ReLU, per frame."""
    cache, outs = [], []
    for fid, x in enumerate(inputs):
        x = x[None, None]
        with torch.no_grad():
            feat = model.forward_features(x)
            if fid == 0:
                d, h = model.forward_depth(feat, x.shape)
                cache = [h] * INFER_LEN
            else:
                cur = cache[0:2] + cache[-INFER_LEN + 3:]
                cc = [torch.cat([e[i] for e in cur], dim=1) for i in range(len(cur[0]))]
                d, h = model.forward_depth(feat, x.shape, cached_hidden_state_list=cc)
                cache.append(h)
        if fid + INFER_LEN > model.gap + 1:
            del cache[1]
        outs.append(d[0, -1].clone())
    return outs

mode = sys.argv[1]
torch.manual_seed(3)
if mode == 'tiny':
    m = VideoDepthAnything(encoder='vits', features=64, out_channels=[48, 96, 192, 384])
    m.pretrained = DinoVisionTransformer(img_size=518, patch_size=14, embed_dim=24, depth=12, num_heads=2,
        mlp_ratio=4, block_fn=functools.partial(Block, attn_class=MemEffAttention), init_values=1.0,
        interpolate_antialias=False, interpolate_offset=0.1, block_chunks=0)
    m.head = DPTHeadTemporal(24, 32, False, out_channels=[32, 32, 32, 32], use_clstoken=False, num_frames=32, pe='ape')
    with torch.no_grad():
        for p in m.parameters():
            p.normal_(0, 0.15)
    m.eval()
    torch.save(collections.OrderedDict((k, v) for k, v in m.state_dict().items()), sys.argv[2])
    inputs = [torch.randn(3, 28, 42) for _ in range(14)]
    out = sys.argv[3]
else:
    enc = sys.argv[5] if len(sys.argv) > 5 else 'vits'
    m = VideoDepthAnything(encoder=enc, **{
        'vits': dict(features=64, out_channels=[48, 96, 192, 384]),
        'vitb': dict(features=128, out_channels=[96, 192, 384, 768]),
        'vitl': dict(features=256, out_channels=[256, 512, 1024, 1024])}[enc])
    m.load_state_dict(torch.load(sys.argv[2], map_location='cpu', weights_only=True), strict=True)
    m.eval()
    import cv2
    from torchvision.transforms import Compose
    from video_depth_anything.util.transform import Resize, NormalizeImage, PrepareForNet
    tf = Compose([Resize(width=518, height=518, resize_target=False, keep_aspect_ratio=True, ensure_multiple_of=14,
                         resize_method='lower_bound', image_interpolation_method=cv2.INTER_CUBIC),
                  NormalizeImage(mean=[0.485, 0.456, 0.406], std=[0.229, 0.224, 0.225]), PrepareForNet()])
    inputs = []
    for f in sorted(glob.glob(os.path.join(sys.argv[3], '*.ppm'))):
        img = cv2.cvtColor(cv2.imread(f), cv2.COLOR_BGR2RGB).astype(np.float32) / 255.0
        inputs.append(torch.from_numpy(tf({'image': img})['image']))
    out = sys.argv[4]
outs = stream(m, inputs)
io = collections.OrderedDict()
for i, (x, y) in enumerate(zip(inputs, outs)):
    io[f'in.{i}'], io[f'out.{i}'] = x.contiguous(), y.contiguous()
torch.save(io, out)
print(len(inputs), 'frames', tuple(inputs[0].shape), '->', tuple(outs[0].shape), float(outs[-1].mean()))
