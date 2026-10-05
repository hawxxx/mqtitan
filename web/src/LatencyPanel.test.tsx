import {render,screen,cleanup} from '@testing-library/react';
import {describe,it,expect,afterEach} from 'vitest';
import {LatencyPanel} from './LatencyPanel';
import type {Snapshot} from './model';
afterEach(cleanup);

describe('LatencyPanel',()=>{
 it('shows actual percentile values and measured histogram samples',()=>{render(<LatencyPanel snapshot={{p50:1e6,p75:2e6,p90:3e6,p95:4e6,p99:5e6,p999:9e6,publishLatencyCount:50,histogram:[{upperBoundNs:1e6,count:40},{upperBoundNs:1e7,count:10}]} as Snapshot}/>);expect(screen.getByText('9.00 ms')).toBeTruthy();expect(screen.getByText('50 measured publish samples')).toBeTruthy();expect(screen.getByRole('img',{name:'Publish latency histogram'})).toBeTruthy()});
 it('does not fabricate a distribution without observations',()=>{render(<LatencyPanel snapshot={{p50:0,p95:0,p99:0} as Snapshot}/>);expect(screen.getByText('No publish latency samples yet')).toBeTruthy();expect(screen.queryByRole('img')).toBeNull()});
});
