#!/usr/bin/env python3
"""Evaluate the shipped dashboard queries against deterministic Prometheus fixtures."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
dashboard = json.loads((ROOT / 'dashboards/oci-compute-availability.json').read_text())
panels = {p['title']: p for p in dashboard['panels']}
values = {'$job': 'capacity', '$instance': 'fixture', '${region:regex}': '.*', '${shape:regex}': '.*', '${ocpus:regex}': '.*', '${memory:regex}': '.*', '${freshness:raw}': '900'}
def render(expr):
    for key, value in values.items():
        expr = expr.replace(key, value)
    assert '$' not in expr, expr
    return expr

def labels(name, instance='fixture'):
    return 'job="capacity",instance="' + instance + '",query="' + name + '",region="us-ashburn-1",availability_domain="AD-1",shape="VM.Standard.E6.Flex",ocpus="2",memory_gbs="8"'

series = []
for name, state, checked, count in [('good',1,0,7),('failed',0,0,None),('full',2,0,0),('unsupported',3,0,None),('old',1,-1000,9)]:
    for metric, value in [('oci_capacity_state',state),('oci_capacity_checked_timestamp_seconds',checked),('oci_capacity_available_count',count)]:
        if value is not None:
            series.append({'series': metric+'{'+labels(name)+'}', 'values': f'{value}+0x30'})
# A different collector must never be mixed into the selected customer's view.
series.extend([{'series': metric+'{'+labels('other','another-customer')+'}', 'values': f'{value}+0x30'} for metric,value in [('oci_capacity_state',1),('oci_capacity_checked_timestamp_seconds',0),('oci_capacity_available_count',99)]])
series.extend([{'series':'up{job="capacity",instance="fixture"}','values':'1+0x30'},{'series':'oci_capacity_collection_success{job="capacity",instance="fixture"}','values':'0+0x30'},{'series':'oci_capacity_collection_errors{job="capacity",instance="fixture"}','values':'1+0x30'}])

def samples(items):
    return [{'labels':'{'+labels(name)+'}', 'value':value} for name,value in items]

tests=[]
def check(panel, ref, at, expected):
    expr = next(t['expr'] for t in panels[panel]['targets'] if t['refId']==ref)
    tests.append({'expr':render(expr), 'eval_time':at, 'exp_samples':samples(expected)})

check('All deployment observations','A','5m',[('good',1),('failed',0),('full',2),('unsupported',3),('old',4)])
check('All deployment observations','A','20m',[(n,4) for n in ('good','failed','full','unsupported','old')])
check('Fresh options for new instances','A','5m',[('good',1)])
check('Fresh options for new instances','A','20m',[])
check('Fresh options for new instances','C','5m',[('good',7)])
check('Fresh options for new instances','C','20m',[])
check('All deployment observations','C','5m',[('good',7),('full',0),('old',9)])
check('All deployment observations','B','5m',[(n,300 if n!='old' else 1300) for n in ('good','failed','full','unsupported','old')])
# Compile and execute every panel expression, including currently empty error/history panels.
# Recording rules need no expected results and are evaluated by promtool during this suite.
rules=[{'record':f'dashboard_expression_{p["id"]}_{i}', 'expr':render(t['expr'])} for p in dashboard['panels'] for i,t in enumerate(p.get('targets',[]))]
with tempfile.TemporaryDirectory(prefix='.capacity-dashboard-', dir=ROOT) as temp:
    path=Path(temp)
    path.chmod(0o755)
    (path/'rules.json').write_text(json.dumps({'groups':[{'name':'dashboard','rules':rules}]}))
    (path/'tests.json').write_text(json.dumps({'rule_files':['rules.json'],'evaluation_interval':'1m','tests':[{'interval':'1m','input_series':series,'promql_expr_test':tests}]}))
    promtool=shutil.which('promtool')
    if promtool:
        cmd=[promtool,'test','rules',str(path/'tests.json')]
    else:
        image=os.environ.get('PROMTOOL_IMAGE','prom/prometheus@sha256:e04987641b57704f408e31f6373ba4138ecf27cb4a165d24aabb6c38fddd0f06')
        cmd=['docker','run','--rm','--entrypoint','/bin/promtool','-v',str(path)+':/tests:ro','-w','/tests',image,'test','rules','tests.json']
    subprocess.run(cmd,check=True,cwd=path)
print('Dashboard queries passed: partial failure, stale data, unknown counts, and collector isolation.')
