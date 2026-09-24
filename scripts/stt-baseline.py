import ctypes, ctypes.wintypes as w, json, os, pathlib, subprocess, time, wave
# Exact pre-runtime provider flags. Windows-only; synthetic silence is latency-only.
# Output/audio stays ignored under runtime/whisper/bench.
root=pathlib.Path(__file__).resolve().parents[1]/'runtime'/'whisper'
(root/'bench').mkdir(parents=True,exist_ok=True)
class Memory(ctypes.Structure):
    _fields_=[('cb',w.DWORD),('PageFaultCount',w.DWORD)]+[(n,ctypes.c_size_t) for n in ['PeakWorkingSetSize','WorkingSetSize','QuotaPeakPagedPoolUsage','QuotaPagedPoolUsage','QuotaPeakNonPagedPoolUsage','QuotaNonPagedPoolUsage','PagefileUsage','PeakPagefileUsage']]
kernel=ctypes.WinDLL('kernel32'); psapi=ctypes.WinDLL('psapi')
kernel.GetProcessTimes.argtypes=[w.HANDLE]+[ctypes.POINTER(w.FILETIME)]*4
psapi.GetProcessMemoryInfo.argtypes=[w.HANDLE,ctypes.POINTER(Memory),w.DWORD]
records=[]
for seconds in (2,5,10,30):
    for repeat in range(2):
        start=time.perf_counter()
        wav=root/'bench'/f'silence-{seconds}.wav'
        with wave.open(str(wav),'wb') as f:
            f.setnchannels(1); f.setsampwidth(2); f.setframerate(16000); f.writeframes(bytes(32000*seconds))
        wav_ms=(time.perf_counter()-start)*1000
        with open(root/'bench'/'stdout.tmp','wb') as out, open(root/'bench'/'stderr.tmp','wb') as err:
            spawned=time.perf_counter()
            p=subprocess.Popen([str(root/'whisper-cli.exe'),'-m',str(root/'models'/'ggml-small.bin'),'-f',str(wav),'-l','ja','-nt','-np'],stdout=out,stderr=err)
            launch_ms=(time.perf_counter()-spawned)*1000; peak=0
            while p.poll() is None:
                m=Memory();m.cb=ctypes.sizeof(m)
                if psapi.GetProcessMemoryInfo(int(p._handle),ctypes.byref(m),m.cb):peak=max(peak,m.PeakWorkingSetSize)
                time.sleep(.01)
            wall=time.perf_counter()-start
            times=[w.FILETIME() for _ in range(4)]
            cpu=None
            if kernel.GetProcessTimes(int(p._handle),*[ctypes.byref(v) for v in times]):
                cpu=sum((v.dwHighDateTime<<32)+v.dwLowDateTime for v in times[2:])/1e7
        row=dict(case='A',fixture='synthetic-silence-latency-only',audio_seconds=seconds,repeat=repeat+1,model='small',language='ja',device='cpu',persistent=False,total_ms=wall*1000,rtf=wall/seconds,wav_write_ms=wav_ms,process_launch_api_ms=launch_ms,process_startup_ms=None,model_load_ms=None,inference_ms=None,eot_to_final_ms=wall*1000,first_partial_ms=None,finalization_ms=None,peak_working_set_bytes=peak or None,process_cpu_seconds=cpu,cpu_percent_one_core=cpu/wall*100 if cpu is not None else None,exit_code=p.returncode)
        records.append(row); print(json.dumps(row),flush=True)
        (root/'bench'/'baseline.json').write_text(json.dumps(records,indent=2),encoding='utf8')
