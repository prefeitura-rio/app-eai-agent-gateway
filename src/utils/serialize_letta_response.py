from typing import Any

def serialize_letta_response(obj: Any) -> Any:
    """
    Serializa objetos complexos do Letta para formatos JSON-compatíveis
    """
    if hasattr(obj, '__dict__'):
        # Se o objeto tem __dict__, converte para dicionário
        return {key: serialize_letta_response(value) for key, value in obj.__dict__.items()}
    elif hasattr(obj, 'model_dump'):
        # Se é um modelo Pydantic, usa model_dump
        return obj.model_dump()
    elif hasattr(obj, 'dict'):
        # Se tem método dict(), usa ele
        return obj.dict()
    elif isinstance(obj, (list, tuple)):
        # Se é lista ou tupla, serializa cada elemento
        return [serialize_letta_response(item) for item in obj]
    elif isinstance(obj, dict):
        # Se é dicionário, serializa cada valor
        return {key: serialize_letta_response(value) for key, value in obj.items()}
    elif isinstance(obj, (str, int, float, bool, type(None))):
        # Tipos básicos já são serializáveis
        return obj
    else:
        # Fallback: converte para string
        return str(obj)
