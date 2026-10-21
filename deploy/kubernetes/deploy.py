#!/usr/bin/env python3
"""Update the existing single-owner GKE deployment without publishing runtime data."""
import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path

STATUS = r'''
import json, urllib.request, hashlib, pathlib
s=json.load(urllib.request.urlopen('http://127.0.0.1:7341/api/v1/status', timeout=10))
print(json.dumps({
 'database':next((name for name in ['jobs.db','factory.db'] if (pathlib.Path('/var/lib/machinist')/name).is_file()),None),
 'jobs':{j['id']:{'state':j['state'],'hold':j.get('publication_hold',False)} for j in s['jobs']},
 'automations':{a['id']:{'paused':a.get('paused',False),'scheduled':bool(a.get('cron')),'definition':hashlib.sha256(json.dumps({k:a.get(k) for k in ['name','repository','prompt','cron','timezone','kind','version']},sort_keys=True).encode()).hexdigest()} for a in s.get('automations',[])},
 'repositories':s.get('repositories',[]),
 'connected':any(w.get('connected') for w in s.get('workers',[]))}))
'''
BACKUP = r'''
import pathlib, sqlite3, sys, os
root=pathlib.Path('/var/lib/machinist')
source_path=next((root/name for name in ['jobs.db','factory.db'] if (root/name).is_file()),None)
if source_path is None: raise RuntimeError('No authoritative SQLite database exists')
target=root/'deployments'/sys.argv[1]
target.mkdir(parents=True,exist_ok=False,mode=0o700)
with sqlite3.connect('file:'+str(source_path)+'?mode=ro',uri=True) as source, sqlite3.connect(target/'jobs.db') as backup:
 source.backup(backup)
 if backup.execute('pragma integrity_check').fetchone()[0]!='ok': raise RuntimeError('SQLite backup integrity check failed')
os.chmod(target/'jobs.db',0o600)
(target/'release.json').write_text(sys.argv[2])
print('Private SQLite backup completed; runtime will be captured by the disk snapshot.')
'''
CONFIG = r'''
import pathlib,tomllib,json,re,sys,os,stat
p=pathlib.Path('/var/lib/machinist/config.toml'); text=p.read_text(); before=tomllib.loads(text)
image=sys.argv[1]
if not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}',image): raise ValueError('Runner image must be digest pinned')
pattern=r'(?ms)(^\[server\.kubernetes\][ \t]*\n)(.*?)(?=^\[|\Z)'
def replace(m):
 body,n=re.subn(r'(?m)^image[ \t]*=.*$', 'image = '+json.dumps(image),m[2])
 if n!=1: raise ValueError('Expected one Kubernetes runner image setting')
 return m[1]+body
updated,n=re.subn(pattern,replace,text)
if n!=1: raise ValueError('Expected one Kubernetes configuration section')
after=tomllib.loads(updated); before['server']['kubernetes']['image']=image
if before!=after: raise ValueError('Configuration changed beyond runner image')
tmp=p.with_suffix('.deploy.tmp')
with open(tmp,'w') as f:
 os.fchmod(f.fileno(),stat.S_IMODE(p.stat().st_mode)); f.write(updated); f.flush(); os.fsync(f.fileno())
os.replace(tmp,p)
print('Runner digest updated; other runtime configuration preserved.')
'''
CHECK = r'''
import json, urllib.request
base='http://127.0.0.1:7341'
s=json.load(urllib.request.urlopen(base+'/api/v1/status',timeout=10))
r=urllib.request.Request(base+'/api/v1/onboarding/check',data=b'{}',headers={'Content-Type':'application/json','Origin':base,'X-Machinist-CSRF':s['csrf_token']})
a=json.load(urllib.request.urlopen(r,timeout=240))
if a.get('state')!='passed': raise RuntimeError('Runner connection check failed: '+a.get('message','unknown error'))
print('Real Kubernetes runner connection check passed.')
'''


def pinned(image):
    if not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}', image):
        raise ValueError('Images must be pinned by sha256 digest')
    return image


def require_idle(status):
    if not status.get('database'):
        raise RuntimeError('No supported database exists; refusing to stop the owner')
    # Awaiting-merge work can enqueue repairs through GitHub follow-up polling.
    if any(j['state'] in {'running','queued','leased','cancelling','awaiting_merge'} for j in status['jobs'].values()):
        raise RuntimeError('Deployment postponed: finish active work and PR follow-ups first')
    if any(a['scheduled'] and not a['paused'] for a in status['automations'].values()):
        raise RuntimeError('Deployment postponed: pause active schedules first')


def preserved(before, after):
    if set(before['jobs']) != set(after['jobs']):
        raise RuntimeError('Saved job history changed during deployment')
    if any(j['hold'] != after['jobs'][key]['hold'] for key,j in before['jobs'].items()):
        raise RuntimeError('Publication holds changed during deployment')
    if before['automations'] != after['automations'] or before['repositories'] != after['repositories']:
        raise RuntimeError('Projects or automation settings changed during deployment')


class Deployment:
    def __init__(self, context):
        self.context = context

    def k(self, *args, namespace='machinist-system', data=None, timeout=360):
        result=subprocess.run(['kubectl','--context',self.context,'-n',namespace,*args],input=json.dumps(data) if data is not None else None,text=True,capture_output=True,timeout=timeout)
        if result.returncode:
            raise RuntimeError('kubectl '+args[0]+' failed: '+result.stderr.strip())
        return result.stdout

    def get(self, kind, name=None, **kwargs):
        return json.loads(self.k('get',kind,*([name] if name else []),'-o','json',**kwargs))

    def status(self):
        return json.loads(self.k('exec','deployment/machinist-control-plane','--','python3','-c',STATUS))

    def no_runners(self):
        for kind,selector in [('jobs','app.kubernetes.io/managed-by=machinist'),('pods','app=machinist-agent'),('configmaps','app.kubernetes.io/managed-by=machinist')]:
            objects=json.loads(self.k('get',kind,'-l',selector,'-o','json',namespace='machinist-runners'))
            if objects['items']:
                raise RuntimeError('Runner '+kind+' remain; do not bypass uncertain process ownership')

    def run(self, control_plane, runner, revision, output):
        pinned(control_plane); pinned(runner)
        if not re.fullmatch('[a-f0-9]{40}',revision):
            raise ValueError('Revision must be a full commit SHA')
        before=self.status(); require_idle(before); self.no_runners()
        old=self.get('deployment','machinist-control-plane')
        if old['spec']['replicas']!=1 or old['spec']['strategy']['type']!='Recreate':
            raise RuntimeError('Expected one Recreate control-plane owner')
        old_image=old['spec']['template']['spec']['containers'][0]['image']
        proxy=self.get('deployment','machinist-egress',namespace='machinist-runners')
        name='machinist-deploy-'+revision[:8]+'-'+str(int(time.time()))
        release={'revision':revision,'control_plane':control_plane,'runner':runner,'previous_control_plane':old_image,'previous_proxy':proxy['spec']['template']['spec']['containers'][0]['image'],'snapshot':name,'jobs_preserved':len(before['jobs'])}
        self.k('scale','deployment/machinist-control-plane','--replicas=0')
        self.k('wait','--for=delete','pod','-l','app=machinist-control-plane','--timeout=180s')
        self.no_runners()
        security={'runAsNonRoot':True,'runAsUser':1000,'runAsGroup':1000,'fsGroup':1000,'seccompProfile':{'type':'RuntimeDefault'}}
        pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':name},'spec':{'restartPolicy':'Never','activeDeadlineSeconds':900,'automountServiceAccountToken':False,'securityContext':security,'containers':[{'name':'utility','image':old_image,'command':['python3','-c','import time; time.sleep(900)'],'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},'resources':{'requests':{'cpu':'250m','memory':'512Mi','ephemeral-storage':'1Gi'},'limits':{'cpu':'250m','memory':'512Mi','ephemeral-storage':'1Gi'}},'volumeMounts':[{'name':'state','mountPath':'/var/lib/machinist'}]}],'volumes':[{'name':'state','persistentVolumeClaim':{'claimName':'machinist-state'}}]}}
        # Once stopped, failures deliberately leave the owner stopped. Never
        # restart an old binary against potentially migrated or changed state.
        try:
            self.k('create','-f','-',data=pod)
            self.k('wait','--for=condition=Ready','pod/'+name,'--timeout=300s')
            self.k('exec',name,'--','python3','-c',BACKUP,name,json.dumps(release))
            snapshot={'apiVersion':'snapshot.storage.k8s.io/v1','kind':'VolumeSnapshot','metadata':{'name':name},'spec':{'volumeSnapshotClassName':'machinist-state-retain','source':{'persistentVolumeClaimName':'machinist-state'}}}
            self.k('create','-f','-',data=snapshot)
            self.k('wait','--for=jsonpath={.status.readyToUse}=true','volumesnapshot/'+name,'--timeout=300s')
            self.k('exec',name,'--','python3','-c',CONFIG,runner)
        finally:
            self.k('delete','pod',name,'--ignore-not-found=true','--wait=true','--timeout=180s')
        self.k('set','image','deployment/machinist-egress','proxy='+runner,namespace='machinist-runners')
        self.k('rollout','status','deployment/machinist-egress','--timeout=300s',namespace='machinist-runners')
        self.k('set','image','deployment/machinist-control-plane','machinist='+control_plane)
        self.k('annotate','deployment/machinist-control-plane','machinist.dev/revision='+revision,'machinist.dev/state-snapshot='+name,'--overwrite')
        self.k('scale','deployment/machinist-control-plane','--replicas=1')
        self.k('rollout','status','deployment/machinist-control-plane','--timeout=300s')
        expected=time.monotonic()+90
        while True:
            after=self.status()
            if after['connected']:break
            if time.monotonic()>expected:raise RuntimeError('Dispatcher did not reconnect')
            time.sleep(3)
        preserved(before,after)
        self.k('exec','deployment/machinist-control-plane','--','python3','-c',CHECK)
        self.no_runners()
        Path(output).write_text(json.dumps(release,indent=2)+'\n')
        print(json.dumps(release,indent=2))


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--context',required=True)
    p.add_argument('--control-plane',required=True,type=pinned)
    p.add_argument('--runner',required=True,type=pinned)
    p.add_argument('--revision',required=True)
    p.add_argument('--output',default='release.json')
    a=p.parse_args()
    try:Deployment(a.context).run(a.control_plane,a.runner,a.revision,a.output)
    except (RuntimeError,ValueError,subprocess.SubprocessError) as error:
        print(str(error),file=sys.stderr)
        print('Deployment stopped. Inspect the current owner and snapshot before recovery; no state was discarded.',file=sys.stderr)
        sys.exit(1)
