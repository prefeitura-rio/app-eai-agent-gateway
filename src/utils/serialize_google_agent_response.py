import uuid
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from loguru import logger


def serialize_google_agent_response(messages: List[Dict], usage: Dict, agent_id: str, processed_at: str) -> Dict:
    """
    Serializa a resposta do Google Agent Engine para o formato padrão do Letta.
    
    Args:
        messages: Lista de mensagens do Google Agent Engine
        usage: Informações de usage do Google Agent Engine
        agent_id: ID do agente
        processed_at: ID do processamento
        
    Returns:
        Dict no formato padrão do Letta
    """
    serialized_messages = []
    current_step_id = f"step-{uuid.uuid4()}"
    
    for i, message in enumerate(messages):
        message_type = message.get("type", "unknown")
        
        if message_type == "human":
            # Mensagem do usuário
            serialized_message = {
                "id": f"message-{uuid.uuid4()}",
                "date": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S+00:00"),
                "name": None,
                "message_type": "user_message",
                "otid": str(uuid.uuid4()),
                "sender_id": None,
                "step_id": current_step_id,
                "is_err": None,
                "content": message.get("content", "")
            }
            
        elif message_type == "ai":
            content = message.get("content", "")
            tool_calls = message.get("tool_calls", [])
            
            if tool_calls:
                # Mensagem com chamada de ferramenta
                for tool_call in tool_calls:
                    # Mensagem de reasoning (se houver reasoning tokens)
                    usage_metadata = message.get("usage_metadata", {})
                    output_token_details = usage_metadata.get("output_token_details", {})
                    reasoning_tokens = output_token_details.get("reasoning", 0)
                    
                    if reasoning_tokens > 0:
                        reasoning_message = {
                            "id": f"message-{uuid.uuid4()}",
                            "date": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S+00:00"),
                            "name": None,
                            "message_type": "reasoning_message",
                            "otid": str(uuid.uuid4()),
                            "sender_id": None,
                            "step_id": current_step_id,
                            "is_err": None,
                            "source": "reasoner_model",
                            "reasoning": f"Processando chamada para ferramenta {tool_call.get('name', 'unknown')}",
                            "signature": None
                        }
                        serialized_messages.append(reasoning_message)
                    
                    # Mensagem de tool call
                    tool_call_message = {
                        "id": f"message-{uuid.uuid4()}",
                        "date": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S+00:00"),
                        "name": None,
                        "message_type": "tool_call_message",
                        "otid": str(uuid.uuid4()),
                        "sender_id": None,
                        "step_id": current_step_id,
                        "is_err": None,
                        "tool_call": {
                            "name": tool_call.get("name", "unknown"),
                            "arguments": str(tool_call.get("args", {})),
                            "tool_call_id": tool_call.get("id", str(uuid.uuid4()))
                        }
                    }
                    serialized_messages.append(tool_call_message)
                    
            elif content:
                # Mensagem de resposta do assistente
                usage_metadata = message.get("usage_metadata", {})
                output_token_details = usage_metadata.get("output_token_details", {})
                reasoning_tokens = output_token_details.get("reasoning", 0)
                
                # Adiciona reasoning se houver
                if reasoning_tokens > 0:
                    reasoning_message = {
                        "id": f"message-{uuid.uuid4()}",
                        "date": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S+00:00"),
                        "name": None,
                        "message_type": "reasoning_message",
                        "otid": str(uuid.uuid4()),
                        "sender_id": None,
                        "step_id": current_step_id,
                        "is_err": None,
                        "source": "reasoner_model",
                        "reasoning": "Processando resposta para o usuário",
                        "signature": None
                    }
                    serialized_messages.append(reasoning_message)
                
                # Mensagem do assistente
                assistant_message = {
                    "id": f"message-{uuid.uuid4()}",
                    "date": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S+00:00"),
                    "name": None,
                    "message_type": "assistant_message",
                    "otid": str(uuid.uuid4()),
                    "sender_id": None,
                    "step_id": current_step_id,
                    "is_err": None,
                    "content": content
                }
                serialized_messages.append(assistant_message)
                
        elif message_type == "tool":
            # Resposta de ferramenta
            status = "error" if message.get("status") == "error" else "success"
            tool_return_message = {
                "id": f"message-{uuid.uuid4()}",
                "date": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S+00:00"),
                "name": message.get("name", "unknown_tool"),
                "message_type": "tool_return_message",
                "otid": str(uuid.uuid4()),
                "sender_id": None,
                "step_id": current_step_id,
                "is_err": status == "error",
                "tool_return": message.get("content", ""),
                "status": status,
                "tool_call_id": message.get("tool_call_id", ""),
                "stdout": None,
                "stderr": message.get("content", "") if status == "error" else None
            }
            serialized_messages.append(tool_return_message)
            
            # Gera novo step_id após tool return
            current_step_id = f"step-{uuid.uuid4()}"
    
    # Calcula informações de usage no formato Letta
    letta_usage = {
        "message_type": "usage_statistics",
        "completion_tokens": usage.get("output_tokens", 0),
        "prompt_tokens": usage.get("input_tokens", 0),
        "total_tokens": usage.get("total_tokens", 0),
        "step_count": len(set(msg.get("step_id") for msg in serialized_messages if msg.get("step_id"))),
        "steps_messages": None,
        "run_ids": None
    }
    
    return {
        "status": "completed",
        "data": {
            "messages": serialized_messages,
            "usage": letta_usage,
            "agent_id": agent_id,
            "processed_at": processed_at,
            "status": "done"
        }
    }


def extract_final_assistant_message(serialized_response: Dict) -> Optional[str]:
    """
    Extrai a mensagem final do assistente da resposta serializada.
    
    Args:
        serialized_response: Resposta serializada no formato Letta
        
    Returns:
        Conteúdo da última mensagem do assistente ou None
    """
    messages = serialized_response.get("data", {}).get("messages", [])
    
    # Procura pela última mensagem do assistente
    for message in reversed(messages):
        if message.get("message_type") == "assistant_message":
            return message.get("content", "")
    
    return None