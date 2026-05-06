with open("./model/iroh/iroh_ffi.go", "r") as f:
    code = f.read()

# Replace map[[]byte]ProtocolCreator with map[string]ProtocolCreator
code = code.replace("map[[]byte]ProtocolCreator", "map[string]ProtocolCreator")

# Fix reading key
code = code.replace("key := FfiConverterBytesINSTANCE.Read(reader)\n\t\tvalue := FfiConverterProtocolCreatorINSTANCE.Read(reader)\n\t\tresult[key] = value",
                    "key := FfiConverterBytesINSTANCE.Read(reader)\n\t\tvalue := FfiConverterProtocolCreatorINSTANCE.Read(reader)\n\t\tresult[string(key)] = value")

# Fix writing key
code = code.replace("for key, value := range mapValue {\n\t\tFfiConverterBytesINSTANCE.Write(writer, key)\n\t\tFfiConverterProtocolCreatorINSTANCE.Write(writer, value)",
                    "for key, value := range mapValue {\n\t\tFfiConverterBytesINSTANCE.Write(writer, []byte(key))\n\t\tFfiConverterProtocolCreatorINSTANCE.Write(writer, value)")

# Fix Destroy key
code = code.replace("for key, value := range mapValue {\n\t\tFfiDestroyerBytes{}.Destroy(key)\n\t\tFfiDestroyerProtocolCreator{}.Destroy(value)",
                    "for key, value := range mapValue {\n\t\tFfiDestroyerBytes{}.Destroy([]byte(key))\n\t\tFfiDestroyerProtocolCreator{}.Destroy(value)")

with open("./model/iroh/iroh_ffi.go", "w") as f:
    f.write(code)
