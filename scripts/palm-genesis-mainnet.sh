#!/bin/sh

rm -f genesis.json

./polygon-edge genesis \
--chain-id 11297108109 \
--name "palm" \
--block-gas-limit 18800000 \
--epoch-size 720 \
--epoch-reward "0x473F1317100B34000" \
--consensus polybft \
--min-validator-count 4 \
--sprint-size 360 \
--block-time 5s \
--reward-wallet 0x276DA6a8cfaabf5070a6c0d2E3B9e9256E499E5c \
--native-token-config "Palm Token:PALM:18:true:0xDf4FE27cb676166a78EAE18bdB1D6Dc996635A50" \
--premine 0x276DA6a8cfaabf5070a6c0d2E3B9e9256E499E5c:1000000000000000000000000000 \
--premine 0x0 \
--validators /ip4/94.156.203.199/tcp/30301/p2p/16Uiu2HAm9ziqXTQCFSAxL1VDLcZY3gteHNyvQYohMRcXMnjNKXDh:0xb7c332e3133324fd4F4aC13c31916fa14fC89d99:16b3aaa292425497e7126ae7000c0dd6ccd274a1edf2b203db790aad6f22be720e7ba7089598015f49739f8cb40d8f03726c3dc22ae8254e8eb4b850156d824c2fd33addfc453357978356af5001ffbf004b78aaa4058a89b1c277247ee1361b163deeca10587b0fdf1d51695a062c73020dee0a87b24cb561e4ae1a38076001 \
--validators /ip4/94.156.202.158/tcp/30301/p2p/16Uiu2HAmKpo4uxqTmH1Ti4zS1pSR7XZ1bto6dMfR9v5FZSJz31Y1:0xD38ef6a0907c1f1eE68122f8A58524Fe6a01983D:2d0ac7d292ade705a8332606ee739411c17f774e336fd7cef84a3f90b1f643fd1e9f1780c4307eb0b78552b7f18a82bdeed7729012a681b45732bdd92f294b551bf042c93d5f4de96d5f5b2cc8e7334de8682db0379f919741863bc6e4a36b332f3895120166d3d8dcd419106dee16c74a015bc07bf67197e21718e108ebe1a1 \
--validators /ip4/94.156.203.83/tcp/30301/p2p/16Uiu2HAmPa98Utcf5t86EE5UPXKJjPSaDxSQSKyjZ89iLzGXM46H:0xB9edf9eE2f2dCFf7bB6Ed7D7070A95FC3a83b257:2c86264e8a030a72853f977db7c8154d491fbb33d82a3d982f001842fce090c02012ee3cd297709860e977f55fc10a042dcd76ac979d8363f61bf79548b75c6a1ba8e434b92eebca441f9f0268fb16ab552ad2ddfa5670e3e91376c1a025c7010784c24f247ec901e8d234e9f47a1213b832922f5e3669798577528a52c52edb \
--validators /ip4/94.156.202.150/tcp/30301/p2p/16Uiu2HAkzRp6ecDjWmzXddJyBaEyWPKjAE4oQzhZ3AtTYEaFLHU5:0x42081dE31772DAE8fB7FdFF0164Bb76171CE28FD:266c7a95a9a52b6715b838c36ba0b094bcd90965981d9c3d9bbd1ab61b1fd62f23bc74bd096f57ff8317baff370f6fb5b5af5d772f7102713b3c080de24269e51879d4b3f4b5b4b74eb2c6a15ce6eb6d02fc237c8491163c38a0f510875a4a4f111c77e0f0bf9ed1927a3a2cc4381bb25583ec6047a054688e4161a670c4f40b

jq -r '.genesis.alloc' genesis.json > genesis-alloc.json && \
jq '.params.engineForks[1].alloc = input' genesis-merge-mainnet.json genesis-alloc.json > genesis-merge.json && \
jq 'del(.genesis.alloc)' genesis.json > genesis-no-alloc.json && \
jq -n 'reduce inputs as $i ({}; . * $i)' genesis-no-alloc.json genesis-merge.json > genesis-merged.json && \
rm -f genesis.json && rm -f genesis-no-alloc.json && rm -f genesis-alloc.json && rm -f genesis-merge.json &&  \
mv genesis-merged.json genesis.json
