#!/bin/bash
# 拼接段片 → 后期链（色差+颗粒，与管线同款滤镜）→ 混入原歌曲 → 成片
set -e
cd "$(dirname "$0")/project"

FF=ffmpeg
# 1) 提取原片音轨（48kHz AAC 直拷）
$FF -y -v error -i ../../../tmp_agi_ref/agi_ref.mp4 -vn -c:a copy song_full.m4a

# 2) concat 免转码拼接（与 pipeline concatSegs 同法）
$FF -y -v error -f concat -safe 0 -i renders/segs/list.txt -c copy renders/main_voice.mp4

# 3) 后期链：轻微色差（rgbashift，同 pipeline.go:1065 参数量级）+ 胶片颗粒（noise）
$FF -y -v error -i renders/main_voice.mp4 \
  -vf "rgbashift=rh=-1:bh=1,noise=alls=6:allf=t" \
  -c:v libx264 -crf 18 -preset medium -pix_fmt yuv420p renders/main_post.mp4

# 4) 混入歌曲（画面直拷，音频 AAC 重封装）
$FF -y -v error -i renders/main_post.mp4 -i song_full.m4a \
  -map 0:v -map 1:a -c:v copy -c:a aac -b:a 192k -shortest \
  -movflags +faststart renders/main.mp4

ffprobe -v error -show_entries format=duration,size -of default=noprint_wrappers=1 renders/main.mp4
echo STITCH_DONE
