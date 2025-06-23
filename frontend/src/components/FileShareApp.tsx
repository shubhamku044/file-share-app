import React, { useState, useEffect, useRef } from 'react';
import { Upload, Download, Wifi, User, X, Check, AlertCircle } from 'lucide-react';

interface Peer {
  name: string;
  ip: string;
  port: string;
  lastSeen: string;
  online: boolean;
}

interface FileTransfer {
  id: string;
  filename: string;
  size: number;
  from: string;
  to: string;
  status: 'pending' | 'accepted' | 'rejected' | 'completed';
}

interface WebSocketMessage {
  type: string;
  data: any;
}

const FileShareApp: React.FC = () => {
  const [peers, setPeers] = useState<Peer[]>([]);
  const [transfers, setTransfers] = useState<FileTransfer[]>([]);
  const [selectedPeer, setSelectedPeer] = useState<Peer | null>(null);
  const [deviceName, setDeviceName] = useState<string>('');
  const [isConnected, setIsConnected] = useState<boolean>(false);
  const [showSendModal, setShowSendModal] = useState<boolean>(false);
  const [dragOver, setDragOver] = useState<boolean>(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    // Connect to WebSocket
    connectWebSocket();

    // Fetch initial data
    fetchDeviceName();
    fetchPeers();

    // Set up polling for peers
    const peerInterval = setInterval(fetchPeers, 5000);

    return () => {
      clearInterval(peerInterval);
      if (wsRef.current) {
        wsRef.current.close();
      }
    };
  }, []);

  const connectWebSocket = () => {
    const ws = new WebSocket('ws://localhost:8080/ws');

    ws.onopen = () => {
      setIsConnected(true);
      console.log('WebSocket connected');
    };

    ws.onmessage = (event) => {
      const message = JSON.parse(event.data);
      handleWebSocketMessage(message);
    };

    ws.onclose = () => {
      setIsConnected(false);
      console.log('WebSocket disconnected');
      // Attempt to reconnect after 3 seconds
      setTimeout(connectWebSocket, 3000);
    };

    ws.onerror = (error) => {
      console.error('WebSocket error:', error);
    };

    wsRef.current = ws;
  };

  const handleWebSocketMessage = (message: WebSocketMessage) => {
    switch (message.type) {
      case 'peer_discovered':
        setPeers(prev => {
          const existing = prev.find(p => p.ip === message.data.ip);
          if (existing) {
            return prev.map(p => p.ip === message.data.ip ? message.data : p);
          }
          return [...prev, message.data];
        });
        break;
      case 'peer_offline':
        setPeers(prev => prev.filter(p => p.ip !== message.data.ip));
        break;
      case 'transfer_request':
        setTransfers(prev => {
          const exists = prev.find(t => t.id === message.data.id);
          return exists ? prev : [...prev, message.data];
        });
        break;
      case 'transfer_accepted':
        setTransfers(prev =>
          prev.map(t => t.id === message.data.id ? { ...t, status: 'accepted' } : t)
        );
        break;
      case 'transfer_rejected':
      case 'transfer_completed':
        setTransfers(prev =>
          prev.map(t => t.id === message.data.id ? message.data : t)
        );
        break;
    }
  };

  const fetchDeviceName = async () => {
    try {
      const response = await fetch('http://localhost:8080/api/device-name');
      const data = await response.json();
      setDeviceName(data.name);
    } catch (error) {
      console.error('Error fetching device name:', error);
    }
  };

  const fetchPeers = async () => {
    try {
      const response = await fetch('http://localhost:8080/api/peers');
      const data = await response.json();
      setPeers(data || []);
    } catch (error) {
      console.error('Error fetching peers:', error);
    }
  };

  const handleSendFile = async (file: File, targetPeer: Peer) => {
    const formData = new FormData();
    formData.append('file', file);
    formData.append('targetIP', targetPeer.ip);

    try {
      const response = await fetch('http://localhost:8080/api/send', {
        method: 'POST',
        body: formData,
      });

      if (response.ok) {
        const transfer = await response.json();
        setTransfers(prev => [...prev, transfer]);
        setShowSendModal(false);
        setSelectedPeer(null);
      } else {
        throw new Error('Failed to send file');
      }
    } catch (error) {
      console.error('Error sending file:', error);
      alert('Error sending file');
    }
  };

  const handleAcceptTransfer = async (transferId) => {
    try {
      await fetch(`http://localhost:8080/api/accept/${transferId}`, {
        method: 'POST',
      });
    } catch (error) {
      console.error('Error accepting transfer:', error);
    }
  };

  const handleRejectTransfer = async (transferId) => {
    try {
      await fetch(`http://localhost:8080/api/reject/${transferId}`, {
        method: 'POST',
      });
    } catch (error) {
      console.error('Error rejecting transfer:', error);
    }
  };

  const handleDownloadFile = (transferId) => {
    window.open(`http://localhost:8080/api/receive/${transferId}`, '_blank');
  };

  const handleDrop = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragOver(false);

    const files = Array.from(event.dataTransfer.files);
    if (files.length > 0 && selectedPeer) {
      handleSendFile(files[0], selectedPeer);
    }
  };

  const handleDragOver = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragOver(true);
  };

  const handleDragLeave = () => {
    setDragOver(false);
  };

  const handleFileSelect = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (file && selectedPeer) {
      handleSendFile(file, selectedPeer);
    }
  };

  const incomingTransfers = transfers.filter(t => t.to === deviceName && t.status === 'pending');
  const outgoingTransfers = transfers.filter(t => t.from === deviceName);

  return (
    <div className="min-h-screen w-full bg-gradient-to-br from-blue-50 to-indigo-100 p-4 md:p-6">
      <div className="w-full mx-auto">
        {/* Header */}
        <div className="bg-white rounded-2xl shadow-xl p-4 md:p-6 mb-4 md:mb-6">
          <div className="flex items-center justify-between">
            <div className="flex items-center space-x-3">
              <div className={`w-3 h-3 rounded-full ${isConnected ? 'bg-green-500' : 'bg-red-500'}`}></div>
              <h1 className="text-xl md:text-2xl font-bold text-gray-800">File Share</h1>
            </div>
            <div className="flex items-center space-x-2 text-gray-600">
              <User className="w-4 h-4 md:w-5 md:h-5" />
              <span className="font-medium text-sm md:text-base truncate max-w-48">{deviceName}</span>
            </div>
          </div>
        </div>

        {/* Main Interface */}
        <div className="grid md:grid-cols-2 xl:grid-cols-4 gap-4 md:gap-6 mb-4 md:mb-6">
          {/* Send Section */}
          <div className="bg-white rounded-2xl shadow-xl p-6 md:p-8">
            <div className="text-center">
              <div className="w-20 h-20 md:w-24 md:h-24 mx-auto mb-4 md:mb-6 bg-gradient-to-br from-blue-500 to-blue-600 rounded-full flex items-center justify-center shadow-lg hover:shadow-xl transition-all duration-300 cursor-pointer transform hover:scale-105"
                onClick={() => setShowSendModal(true)}>
                <Upload className="w-10 h-10 md:w-12 md:h-12 text-white" />
              </div>
              <h2 className="text-lg md:text-xl font-semibold text-gray-800 mb-2">Send</h2>
              <p className="text-gray-600 text-sm md:text-base">Send files to connected devices</p>
              <div className="mt-4 text-xs md:text-sm text-gray-500">
                {peers.length} device{peers.length !== 1 ? 's' : ''} found
              </div>
            </div>
          </div>

          {/* Receive Section */}
          <div className="bg-white rounded-2xl shadow-xl p-6 md:p-8">
            <div className="text-center">
              <div className="w-20 h-20 md:w-24 md:h-24 mx-auto mb-4 md:mb-6 bg-gradient-to-br from-green-500 to-green-600 rounded-full flex items-center justify-center shadow-lg">
                <Download className="w-10 h-10 md:w-12 md:h-12 text-white" />
              </div>
              <h2 className="text-lg md:text-xl font-semibold text-gray-800 mb-2">Receive</h2>
              <p className="text-gray-600 text-sm md:text-base">Receive files from other devices</p>
              <div className="mt-4 text-xs md:text-sm text-gray-500">
                {incomingTransfers.length} pending transfer{incomingTransfers.length !== 1 ? 's' : ''}
              </div>
            </div>
          </div>

          {/* Network Status */}
          <div className="bg-white rounded-2xl shadow-xl p-6 md:p-8">
            <div className="text-center">
              <div className="w-20 h-20 md:w-24 md:h-24 mx-auto mb-4 md:mb-6 bg-gradient-to-br from-purple-500 to-purple-600 rounded-full flex items-center justify-center shadow-lg">
                <Wifi className="w-10 h-10 md:w-12 md:h-12 text-white" />
              </div>
              <h2 className="text-lg md:text-xl font-semibold text-gray-800 mb-2">Network</h2>
              <p className="text-gray-600 text-sm md:text-base">Local network discovery</p>
              <div className="mt-4 text-xs md:text-sm text-gray-500">
                Status: {isConnected ? 'Connected' : 'Disconnected'}
              </div>
            </div>
          </div>

          {/* Statistics */}
          <div className="bg-white rounded-2xl shadow-xl p-6 md:p-8">
            <div className="text-center">
              <div className="w-20 h-20 md:w-24 md:h-24 mx-auto mb-4 md:mb-6 bg-gradient-to-br from-orange-500 to-orange-600 rounded-full flex items-center justify-center shadow-lg">
                <AlertCircle className="w-10 h-10 md:w-12 md:h-12 text-white" />
              </div>
              <h2 className="text-lg md:text-xl font-semibold text-gray-800 mb-2">Activity</h2>
              <p className="text-gray-600 text-sm md:text-base">Transfer statistics</p>
              <div className="mt-4 text-xs md:text-sm text-gray-500">
                {transfers.length} total transfer{transfers.length !== 1 ? 's' : ''}
              </div>
            </div>
          </div>
        </div>

        {/* Connected Devices */}
        {peers.length > 0 && (
          <div className="bg-white rounded-2xl shadow-xl p-4 md:p-6 mb-4 md:mb-6">
            <h3 className="text-lg font-semibold text-gray-800 mb-4 flex items-center">
              <Wifi className="w-5 h-5 mr-2" />
              Connected Devices
            </h3>
            <div className="grid sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-6 gap-4">
              {peers.map((peer) => (
                <div key={peer.ip} className="border rounded-lg p-4 hover:bg-gray-50 transition-colors">
                  <div className="flex items-center justify-between">
                    <div className="min-w-0 flex-1">
                      <div className="font-medium text-gray-800 truncate">{peer.name}</div>
                      <div className="text-sm text-gray-500 truncate">{peer.ip}</div>
                    </div>
                    <div className="w-2 h-2 bg-green-500 rounded-full ml-2"></div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {transfers.filter(t => t.status === 'accepted' && t.to === deviceName).length > 0 && (
          <div className="bg-white rounded-2xl shadow-xl p-6 mt-6">
            <h3 className="text-lg font-semibold text-gray-800 mb-4">Ready to Download</h3>
            <div className="space-y-4">
              {transfers.filter(t => t.status === 'accepted' && t.to === deviceName).map((transfer) => (
                <div key={transfer.id} className="border rounded-lg p-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="font-medium text-gray-800">{transfer.filename}</div>
                      <div className="text-sm text-gray-500">
                        From: {transfer.from} • Size: {(transfer.size / 1024 / 1024).toFixed(2)} MB
                      </div>
                    </div>
                    <button
                      onClick={() => handleDownloadFile(transfer.id)}
                      className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600 transition-colors flex items-center space-x-1"
                    >
                      <Download className="w-4 h-4" />
                      <span>Download</span>
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Pending Transfers */}
        {incomingTransfers.length > 0 && (
          <div className="bg-white rounded-2xl shadow-xl p-6 mb-6">
            <h3 className="text-lg font-semibold text-gray-800 mb-4 flex items-center">
              <AlertCircle className="w-5 h-5 mr-2" />
              Incoming Files
            </h3>
            <div className="space-y-4">
              {incomingTransfers.map((transfer) => (
                <div key={transfer.id} className="border rounded-lg p-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="font-medium text-gray-800">{transfer.filename}</div>
                      <div className="text-sm text-gray-500">
                        From: {transfer.from} • Size: {(transfer.size / 1024 / 1024).toFixed(2)} MB
                      </div>
                    </div>
                    <div className="flex space-x-2">
                      <button
                        onClick={() => handleAcceptTransfer(transfer.id)}
                        className="px-4 py-2 bg-green-500 text-white rounded-lg hover:bg-green-600 transition-colors flex items-center space-x-1"
                      >
                        <Check className="w-4 h-4" />
                        <span>Accept</span>
                      </button>
                      <button
                        onClick={() => handleRejectTransfer(transfer.id)}
                        className="px-4 py-2 bg-red-500 text-white rounded-lg hover:bg-red-600 transition-colors flex items-center space-x-1"
                      >
                        <X className="w-4 h-4" />
                        <span>Reject</span>
                      </button>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Transfer History */}
        {outgoingTransfers.length > 0 && (
          <div className="bg-white rounded-2xl shadow-xl p-6">
            <h3 className="text-lg font-semibold text-gray-800 mb-4">Transfer History</h3>
            <div className="space-y-4">
              {outgoingTransfers.map((transfer) => (
                <div key={transfer.id} className="border rounded-lg p-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="font-medium text-gray-800">{transfer.filename}</div>
                      <div className="text-sm text-gray-500">
                        To: {transfer.to} • Status: {transfer.status}
                      </div>
                    </div>
                    <div className={`px-3 py-1 rounded-full text-xs font-medium ${transfer.status === 'completed' ? 'bg-green-100 text-green-800' :
                      transfer.status === 'accepted' ? 'bg-blue-100 text-blue-800' :
                        transfer.status === 'rejected' ? 'bg-red-100 text-red-800' :
                          'bg-yellow-100 text-yellow-800'
                      }`}>
                      {transfer.status}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Accepted transfers ready for download */}
        {transfers.filter(t => t.status === 'accepted' && t.to === deviceName).length > 0 && (
          <div className="bg-white rounded-2xl shadow-xl p-6 mt-6">
            <h3 className="text-lg font-semibold text-gray-800 mb-4">Ready to Download</h3>
            <div className="space-y-4">
              {transfers.filter(t => t.status === 'accepted' && t.to === deviceName).map((transfer) => (
                <div key={transfer.id} className="border rounded-lg p-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="font-medium text-gray-800">{transfer.filename}</div>
                      <div className="text-sm text-gray-500">
                        From: {transfer.from} • Size: {(transfer.size / 1024 / 1024).toFixed(2)} MB
                      </div>
                    </div>
                    <button
                      onClick={() => handleDownloadFile(transfer.id)}
                      className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600 transition-colors flex items-center space-x-1"
                    >
                      <Download className="w-4 h-4" />
                      <span>Download</span>
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>

      {/* Send Modal */}
      {showSendModal && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-2xl p-6 w-full max-w-md">
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-lg font-semibold text-gray-800">Send File</h3>
              <button
                onClick={() => {
                  setShowSendModal(false);
                  setSelectedPeer(null);
                }}
                className="text-gray-500 hover:text-gray-700"
              >
                <X className="w-6 h-6" />
              </button>
            </div>

            {!selectedPeer ? (
              <div>
                <p className="text-gray-600 mb-4">Select a device to send to:</p>
                <div className="space-y-2">
                  {peers.map((peer) => (
                    <button
                      key={peer.ip}
                      onClick={() => setSelectedPeer(peer)}
                      className="w-full text-left p-3 border rounded-lg hover:bg-gray-50 transition-colors"
                    >
                      <div className="font-medium text-gray-800">{peer.name}</div>
                      <div className="text-sm text-gray-500">{peer.ip}</div>
                    </button>
                  ))}
                </div>
              </div>
            ) : (
              <div>
                <p className="text-gray-600 mb-2">Sending to:</p>
                <div className="bg-gray-50 p-3 rounded-lg mb-4">
                  <div className="font-medium text-gray-800">{selectedPeer.name}</div>
                  <div className="text-sm text-gray-500">{selectedPeer.ip}</div>
                </div>

                <div
                  className={`border-2 border-dashed rounded-lg p-8 text-center transition-colors ${dragOver ? 'border-blue-500 bg-blue-50' : 'border-gray-300'
                    }`}
                  onDrop={handleDrop}
                  onDragOver={handleDragOver}
                  onDragLeave={handleDragLeave}
                >
                  <Upload className="w-12 h-12 text-gray-400 mx-auto mb-4" />
                  <p className="text-gray-600 mb-2">Drop file here or</p>
                  <button
                    onClick={() => fileInputRef.current?.click()}
                    className="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600 transition-colors"
                  >
                    Choose File
                  </button>
                  <input
                    ref={fileInputRef}
                    type="file"
                    onChange={handleFileSelect}
                    className="hidden"
                  />
                </div>

                <button
                  onClick={() => setSelectedPeer(null)}
                  className="w-full mt-4 px-4 py-2 border border-gray-300 text-gray-700 rounded-lg hover:bg-gray-50 transition-colors"
                >
                  Back to Device Selection
                </button>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default FileShareApp;
