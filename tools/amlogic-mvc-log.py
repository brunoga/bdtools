import re,sys
path=sys.argv[1]
ev=[];view=None
for line in open(path):
    m=re.match(r'\[ *([\d.]+)@\d\][-hd] (.*)',line)
    if not m: continue
    t=float(m.group(1)); s=m.group(2)
    if 'probe start' in s: ev.append((t,'PROBE',0,0)); continue
    if 'fatal' in s: ev.append((t,'FATAL',0,0)); continue
    if 'h264mvc_remove' in s: ev.append((t,'REMOVE',0,0)); continue
    mm=re.search(r'round (\d): put_ptr is (\d+) ;view\d_buf_id (?:is )?(\d+)',s)
    if mm: ev.append((t,'REC',int(mm.group(1)),int(mm.group(3)))); continue
    mm=re.search(r'View : (\d+), Buffer',s)
    if mm: view=int(mm.group(1)); continue
    mm=re.search(r'POC -- Buffer : ([0-9a-f]+), POC : ([0-9a-f]+)',s)
    if mm: ev.append((t,view,int(mm.group(1),16),int(mm.group(2),16)))
pics=[e for e in ev if e[1] in (0,1)]
print("sessions:",sum(1 for e in ev if e[1]=='PROBE'),"fatal:",sum(1 for e in ev if e[1]=='FATAL'),"pictures v0/v1:",sum(1 for e in pics if e[1]==0),sum(1 for e in pics if e[1]==1))
i=0; last0=None; n=0; diverged=0; first_div=None
rows=[]
while i<len(pics):
    a=pics[i]
    if a[1]==0 and i+1<len(pics) and pics[i+1][1]==1 and pics[i+1][3]==a[3]:
        b=pics[i+1]; gap=(a[0]-last0)*1000 if last0 else 0
        rows.append((a[3]//2,gap,(b[0]-a[0])*1000,a[2],b[2]))
        if a[2]!=b[2]:
            diverged+=1
            if first_div is None: first_div=a[3]//2
        last0=a[0]; i+=2
    else:
        rows.append((a[3]//2,0,0,a[2],-1 if a[1]==0 else -2)); i+=1
print("paired rows:",len(rows),"buffer-id mismatches:",diverged,"first at display frame",first_div)
print("unpaired:",[(r[0],r[4]) for r in rows if r[4]<0][:20])
print("gaps >150ms:",[(r[0],round(r[1])) for r in rows if r[1]>150][:30])
print("v1 delay >60ms:",[(r[0],round(r[2])) for r in rows if r[2]>60][:30])
if '-v' in sys.argv:
    for r in rows: print(f" f{r[0]:3d} +{r[1]:6.1f} v1 {r[2]:5.1f} bufs {r[3]:x}/{r[4]:x}")
