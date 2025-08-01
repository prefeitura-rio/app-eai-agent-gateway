from typing import Any


def serialize_letta_response(obj: Any) -> Any:
    """
    Serializa objetos complexos do Letta para formatos JSON-compatíveis
    """
    if hasattr(obj, "__dict__"):
        # Se o objeto tem __dict__, converte para dicionário
        return {
            key: serialize_letta_response(value) for key, value in obj.__dict__.items()
        }
    if hasattr(obj, "model_dump"):
        # Se é um modelo Pydantic, usa model_dump
        return obj.model_dump()
    if hasattr(obj, "dict"):
        # Se tem método dict(), usa ele
        return obj.dict()
    if isinstance(obj, list | tuple):
        # Se é lista ou tupla, serializa cada elemento
        return [serialize_letta_response(item) for item in obj]
    if isinstance(obj, dict):
        # Se é dicionário, serializa cada valor
        return {key: serialize_letta_response(value) for key, value in obj.items()}
    if isinstance(obj, str | int | float | bool | type(None)):
        # Tipos básicos já são serializáveis
        return obj
    # Fallback: converte para string
    return str(obj)
