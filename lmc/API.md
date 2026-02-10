curl http://192.168.31.170:6969/api/models
```json
{
    "success": true,
    "data": [
        {
            "filename": "GLM-4.7-Flash-IQ4_NL.gguf",
            "index": 0,
            "name": "GLM-4.7-Flash-IQ4_NL",
            "path": "C:\\LLM\\GLM-4.7-Flash-IQ4_NL.gguf"
        },
        {
            "filename": "HY-MT1.5-1.8B-Q8_0.gguf",
            "index": 1,
            "name": "HY-MT1.5-1.8B-Q8_0",
            "path": "C:\\LLM\\HY-MT1.5-1.8B-Q8_0.gguf"
        },
        {
            "filename": "Qwen3-Coder-Next-Q4_K_M.gguf",
            "index": 2,
            "name": "Qwen3-Coder-Next-Q4_K_M",
            "path": "C:\\LLM\\Qwen3-Coder-Next-Q4_K_M.gguf"
        }
    ]
}
```



curl http://192.168.31.170:6969/api/status
```json
{
    "success": true,
    "data": {
        "loaded": false,
        "model": {
            "path": "",
            "baseName": ""
        },
        "serverPort": 6969
    }
}
```



curl -X POST "http://192.168.31.170:6969/api/load?index=1"
```json
{
    "success": true,
    "message": "Model loading started",
    "data": {
        "path": "C:\\LLM\\HY-MT1.5-1.8B-Q8_0.gguf",
        "baseName": "HY-MT1.5-1.8B-Q8_0"
    }
}
```



curl -X POST  http://192.168.31.170:6969/api/unload{

```json
{
    "success": true,
    "message": "Model unloaded"
}
```



curl http://192.168.31.170:6969/api/health
```json
{
    "status": "ok"
}
```



